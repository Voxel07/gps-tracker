package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxImportPoints = 500000

type importSummary struct {
	Valid      int    `json:"valid"`
	Invalid    int    `json:"invalid"`
	Duplicates int    `json:"duplicates"`
	Added      int    `json:"added"`
	From       *int64 `json:"from"`
	To         *int64 `json:"to"`
}

type importProgress struct {
	Type       string         `json:"type"`
	Stage      string         `json:"stage,omitempty"`
	Percent    int            `json:"percent,omitempty"`
	BytesRead  int64          `json:"bytesRead,omitempty"`
	TotalBytes int64          `json:"totalBytes,omitempty"`
	Processed  int            `json:"processed,omitempty"`
	Total      int            `json:"total,omitempty"`
	Valid      int            `json:"valid,omitempty"`
	Invalid    int            `json:"invalid,omitempty"`
	Added      int            `json:"added,omitempty"`
	Duplicates int            `json:"duplicates,omitempty"`
	Summary    *importSummary `json:"summary,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type countingReader struct {
	r    io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

type fixKey struct {
	time int64
	lat  float64
	lon  float64
}

func (a *app) importTrack(w http.ResponseWriter, r *http.Request, user account) {
	format := r.URL.Query().Get("format")
	if format != "gpx" && format != "csv" {
		problem(w, http.StatusBadRequest, "Choose a GPX or CSV file")
		return
	}
	release, ok := a.acquireImport()
	if !ok {
		problem(w, http.StatusTooManyRequests, "Another import is already in progress. Try again shortly.")
		return
	}
	defer release()
	preview := r.URL.Query().Get("preview") == "1"
	stream := r.URL.Query().Get("stream") == "1"

	var rc *http.ResponseController
	var lastEmit time.Time
	emit := func(event importProgress, force bool) {
		if !stream {
			return
		}
		if !force && time.Since(lastEmit) < 100*time.Millisecond {
			return
		}
		lastEmit = time.Now()
		if data, err := json.Marshal(event); err == nil {
			_, _ = w.Write(append(data, '\n'))
			if rc != nil {
				_ = rc.Flush()
			}
		}
	}
	fail := func(status int, msg string) {
		if stream {
			emit(importProgress{Type: "error", Error: msg}, true)
			return
		}
		problem(w, status, msg)
	}

	if stream {
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		rc = http.NewResponseController(w)
		emit(importProgress{Type: "progress", Stage: "parsing", Percent: 0, TotalBytes: max(int64(0), r.ContentLength)}, true)
	}

	body := http.MaxBytesReader(w, r.Body, 100<<20)
	counter := &countingReader{r: body}
	totalBytes := max(int64(0), r.ContentLength)
	onParseProgress := func(valid, invalid int) {
		if !stream {
			return
		}
		pct := 0
		if totalBytes > 0 {
			pct = min(99, int(counter.read*100/totalBytes))
		}
		emit(importProgress{
			Type: "progress", Stage: "parsing", Percent: pct,
			BytesRead: counter.read, TotalBytes: totalBytes,
			Valid: valid, Invalid: invalid,
		}, false)
	}

	var points []fix
	var invalid int
	var err error
	if format == "gpx" {
		points, invalid, err = parseGPX(counter, onParseProgress)
	} else {
		points, invalid, err = parseCSV(counter, onParseProgress)
	}
	if err != nil {
		fail(http.StatusBadRequest, "Could not read this file. Use a GPX track or a GPSLogger CSV under 100 MiB and 500,000 points.")
		return
	}
	emit(importProgress{
		Type: "progress", Stage: "parsing", Percent: 100,
		BytesRead: counter.read, TotalBytes: max(totalBytes, counter.read),
		Valid: len(points), Invalid: invalid,
	}, true)

	summary := importSummary{Valid: len(points), Invalid: invalid}
	for _, point := range points {
		if summary.From == nil || point.Time < *summary.From {
			start := point.Time
			summary.From = &start
		}
		if summary.To == nil || point.Time > *summary.To {
			end := point.Time
			summary.To = &end
		}
	}
	ctx := r.Context()
	if preview {
		emit(importProgress{
			Type: "progress", Stage: "checking", Percent: 0,
			Processed: 0, Total: len(points), Valid: summary.Valid, Invalid: summary.Invalid,
		}, true)
		seen := make(map[fixKey]struct{}, len(points))
		if summary.From != nil && summary.To != nil {
			rows, err := a.reader().QueryContext(ctx,
				"SELECT recorded_at, lat, lon FROM points WHERE user_id = ? AND estimated = 0 AND recorded_at BETWEEN ? AND ?",
				user.ID, *summary.From, *summary.To)
			if err != nil {
				if stream {
					fail(http.StatusInternalServerError, "The request could not be completed")
				} else {
					serverError(w, err)
				}
				return
			}
			for rows.Next() {
				var key fixKey
				if err := rows.Scan(&key.time, &key.lat, &key.lon); err != nil {
					rows.Close()
					if stream {
						fail(http.StatusInternalServerError, "The request could not be completed")
					} else {
						serverError(w, err)
					}
					return
				}
				seen[key] = struct{}{}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				if stream {
					fail(http.StatusInternalServerError, "The request could not be completed")
				} else {
					serverError(w, err)
				}
				return
			}
			rows.Close()
		}
		for i, point := range points {
			key := fixKey{time: point.Time, lat: point.Lat, lon: point.Lon}
			if _, exists := seen[key]; exists {
				summary.Duplicates++
			} else {
				seen[key] = struct{}{}
				summary.Added++
			}
			if stream && (i+1)%2000 == 0 {
				emit(importProgress{
					Type: "progress", Stage: "checking", Percent: (i + 1) * 100 / max(1, len(points)),
					Processed: i + 1, Total: len(points), Valid: summary.Valid, Invalid: summary.Invalid,
					Added: summary.Added, Duplicates: summary.Duplicates,
				}, false)
			}
		}
		if stream {
			emit(importProgress{
				Type: "progress", Stage: "checking", Percent: 100,
				Processed: len(points), Total: len(points), Valid: summary.Valid, Invalid: summary.Invalid,
				Added: summary.Added, Duplicates: summary.Duplicates,
			}, true)
			emit(importProgress{Type: "done", Summary: &summary}, true)
			return
		}
		jsonResponse(w, http.StatusOK, summary)
		return
	}

	emit(importProgress{
		Type: "progress", Stage: "saving", Percent: 0,
		Processed: 0, Total: len(points), Valid: summary.Valid, Invalid: summary.Invalid,
	}, true)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		if stream {
			fail(http.StatusInternalServerError, "The request could not be completed")
		} else {
			serverError(w, err)
		}
		return
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, insertFix)
	if err != nil {
		if stream {
			fail(http.StatusInternalServerError, "The request could not be completed")
		} else {
			serverError(w, err)
		}
		return
	}
	defer statement.Close()
	prefix := "import-" + newToken() + "-"
	breakPending := false
	var lastStream string
	for i, point := range points {
		if point.Stream != lastStream {
			breakPending = false
		}
		lastStream = point.Stream
		breakPending = breakPending || point.BreakBefore
		res, err := statement.ExecContext(ctx, user.ID, prefix+point.Stream, point.Time, point.Lat, point.Lon, false, breakPending)
		if err != nil {
			if stream {
				fail(http.StatusInternalServerError, "The request could not be completed")
			} else {
				serverError(w, err)
			}
			return
		}
		if count, _ := res.RowsAffected(); count == 0 {
			summary.Duplicates++
		} else {
			summary.Added++
			breakPending = false
		}
		if stream && (i+1)%1000 == 0 {
			emit(importProgress{
				Type: "progress", Stage: "saving", Percent: (i + 1) * 100 / max(1, len(points)),
				Processed: i + 1, Total: len(points), Valid: summary.Valid, Invalid: summary.Invalid,
				Added: summary.Added, Duplicates: summary.Duplicates,
			}, false)
		}
	}
	if err := tx.Commit(); err != nil {
		if stream {
			fail(http.StatusInternalServerError, "The request could not be completed")
		} else {
			serverError(w, err)
		}
		return
	}
	if stream {
		emit(importProgress{
			Type: "progress", Stage: "saving", Percent: 100,
			Processed: len(points), Total: len(points), Valid: summary.Valid, Invalid: summary.Invalid,
			Added: summary.Added, Duplicates: summary.Duplicates,
		}, true)
		emit(importProgress{Type: "done", Summary: &summary}, true)
		return
	}
	jsonResponse(w, http.StatusOK, summary)
}

func parseGPX(body io.Reader, onProgress func(valid, invalid int)) ([]fix, int, error) {
	decoder := xml.NewDecoder(body)
	var points []fix
	var depth, track, segment, invalid int
	var root, inTrack, inSegment, breakBefore bool
	var namespace string
	for {
		token, err := decoder.Token()
		if err == io.EOF && root && depth == 0 {
			return points, invalid, nil
		}
		if err != nil {
			return nil, 0, err
		}
		switch item := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if root || item.Name.Local != "gpx" {
					return nil, 0, errors.New("expected GPX root")
				}
				root, namespace = true, item.Name.Space
			}
			if item.Name.Space != namespace {
				continue
			}
			switch {
			case depth == 2 && item.Name.Local == "trk":
				track++
				segment = 0
				inTrack = true
			case depth == 3 && inTrack && item.Name.Local == "trkseg":
				segment++
				inSegment, breakBefore = true, segment > 1
			case depth == 4 && inSegment && item.Name.Local == "trkpt":
				var point struct {
					Lat  string `xml:"lat,attr"`
					Lon  string `xml:"lon,attr"`
					Time string `xml:"time"`
				}
				if err := decoder.DecodeElement(&point, &item); err != nil {
					return nil, 0, err
				}
				depth--
				f, valid := importedFix(point.Time, point.Lat, point.Lon)
				if valid {
					f.Stream = strconv.Itoa(track)
					f.BreakBefore = breakBefore
					points = append(points, f)
					breakBefore = false
				} else {
					invalid++
				}
				if onProgress != nil && (len(points)+invalid)%1000 == 0 {
					onProgress(len(points), invalid)
				}
			}
		case xml.EndElement:
			if depth == 3 {
				inSegment = false
			}
			if depth == 2 {
				inTrack = false
			}
			depth--
		}
		if len(points)+invalid > maxImportPoints {
			return nil, 0, errors.New("too many GPX points")
		}
	}
}

func parseCSV(body io.Reader, onProgress func(valid, invalid int)) ([]fix, int, error) {
	reader := csv.NewReader(body)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, 0, err
	}
	indices := map[string]int{}
	for i, field := range header {
		indices[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(field, "\ufeff")))] = i
	}
	column := func(names ...string) int {
		for _, name := range names {
			if i, ok := indices[name]; ok {
				return i
			}
		}
		return -1
	}
	ts, lat, lon := column("time", "timestamp", "recorded_at", "datetime"), column("latitude", "lat"), column("longitude", "lon", "lng", "long")
	if min(ts, lat, lon) < 0 {
		return nil, 0, errors.New("CSV requires time, latitude and longitude columns")
	}
	var points []fix
	var invalid int
	for {
		row, err := reader.Read()
		if err == io.EOF {
			return points, invalid, nil
		}
		if err != nil {
			return nil, 0, err
		}
		if len(row) > max(ts, lat, lon) {
			point, valid := importedFix(row[ts], row[lat], row[lon])
			if valid {
				points = append(points, point)
			} else {
				invalid++
			}
		} else {
			invalid++
		}
		if onProgress != nil && (len(points)+invalid)%1000 == 0 {
			onProgress(len(points), invalid)
		}
		if len(points)+invalid > maxImportPoints {
			return nil, 0, errors.New("too many CSV points")
		}
	}
}

func importedFix(timestamp, latitude, longitude string) (fix, bool) {
	ts, timeErr := parseTimestamp(timestamp)
	lat, latErr := strconv.ParseFloat(strings.TrimSpace(latitude), 64)
	lon, lonErr := strconv.ParseFloat(strings.TrimSpace(longitude), 64)
	return fix{Time: ts, Lat: lat, Lon: lon}, timeErr == nil && latErr == nil && lonErr == nil && validCoordinates(lat, lon)
}

func (a *app) exportGPX(w http.ResponseWriter, r *http.Request, user account) {
	start, end, ok := timeRange(r)
	if !ok {
		problem(w, http.StatusBadRequest, "Choose a valid date range")
		return
	}
	ctx := r.Context()
	var count int64
	if err := a.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM points WHERE "+rangeFilter, user.ID, start, end, start, end).Scan(&count); err != nil {
		serverError(w, err)
		return
	}
	if count == 0 {
		problem(w, http.StatusNotFound, "No points found in this date range")
		return
	}
	rows, err := a.reader().QueryContext(ctx, "SELECT "+pointFields+" FROM points WHERE "+rangeFilter+" ORDER BY stream, COALESCE(edited_time, recorded_at), id", user.ID, start, end, start, end)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()

	fromDate := time.UnixMilli(start).UTC().Format("2006-01-02")
	toDate := time.UnixMilli(end - 1).UTC().Format("2006-01-02")
	filename := "gps-tracker-" + fromDate + ".gpx"
	if toDate != fromDate {
		filename = "gps-tracker-" + fromDate + "_to_" + toDate + ".gpx"
	}

	w.Header().Set("Content-Type", "application/gpx+xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)

	bw := bufio.NewWriterSize(w, 32*1024)
	_, _ = bw.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	_, _ = bw.WriteString(`<gpx version="1.1" creator="GPS Tracker" xmlns="http://www.topografix.com/GPX/1/1">` + "\n")
	_, _ = fmt.Fprintf(bw, "  <metadata><time>%s</time></metadata>\n", time.Now().UTC().Format(time.RFC3339))

	var last fix
	trackOpen := false
	segOpen := false
	trackIndex := 0
	for rows.Next() {
		point, err := scanFix(rows)
		if err != nil {
			break
		}
		newStream := !trackOpen || point.Stream != last.Stream
		newSegment := !newStream && isGap(last, point)

		if newStream {
			if segOpen {
				_, _ = bw.WriteString("    </trkseg>\n")
				segOpen = false
			}
			if trackOpen {
				_, _ = bw.WriteString("  </trk>\n")
			}
			trackIndex++
			_, _ = fmt.Fprintf(bw, "  <trk>\n    <name>Track %d</name>\n    <trkseg>\n", trackIndex)
			trackOpen = true
			segOpen = true
		} else if newSegment {
			if segOpen {
				_, _ = bw.WriteString("    </trkseg>\n")
			}
			_, _ = bw.WriteString("    <trkseg>\n")
			segOpen = true
		}

		_, _ = fmt.Fprintf(bw, "      <trkpt lat=\"%.7f\" lon=\"%.7f\"><time>%s</time></trkpt>\n",
			point.Lat, point.Lon, time.UnixMilli(point.Time).UTC().Format(time.RFC3339Nano))
		last = point
	}
	if segOpen {
		_, _ = bw.WriteString("    </trkseg>\n")
	}
	if trackOpen {
		_, _ = bw.WriteString("  </trk>\n")
	}
	_, _ = bw.WriteString("</gpx>\n")
	_ = bw.Flush()
}
