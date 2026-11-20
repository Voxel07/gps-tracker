package main

import (
	"encoding/csv"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	body := http.MaxBytesReader(w, r.Body, 100<<20)
	var points []fix
	var invalid int
	var err error
	if format == "gpx" {
		points, invalid, err = parseGPX(body)
	} else {
		points, invalid, err = parseCSV(body)
	}
	if err != nil {
		problem(w, http.StatusBadRequest, "Could not read this file. Use a GPX track or a GPSLogger CSV under 100 MiB and 500,000 points.")
		return
	}
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
		seen := make(map[fixKey]struct{}, len(points))
		if summary.From != nil && summary.To != nil {
			rows, err := a.reader().QueryContext(ctx,
				"SELECT recorded_at, lat, lon FROM points WHERE user_id = ? AND estimated = 0 AND recorded_at BETWEEN ? AND ?",
				user.ID, *summary.From, *summary.To)
			if err != nil {
				serverError(w, err)
				return
			}
			for rows.Next() {
				var key fixKey
				if err := rows.Scan(&key.time, &key.lat, &key.lon); err != nil {
					rows.Close()
					serverError(w, err)
					return
				}
				seen[key] = struct{}{}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				serverError(w, err)
				return
			}
			rows.Close()
		}
		for _, point := range points {
			key := fixKey{time: point.Time, lat: point.Lat, lon: point.Lon}
			if _, exists := seen[key]; exists {
				summary.Duplicates++
			} else {
				seen[key] = struct{}{}
				summary.Added++
			}
		}
		jsonResponse(w, http.StatusOK, summary)
		return
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, insertFix)
	if err != nil {
		serverError(w, err)
		return
	}
	defer statement.Close()
	prefix := "import-" + newToken() + "-"
	breakPending := false
	var lastStream string
	for _, point := range points {
		if point.Stream != lastStream {
			breakPending = false
		}
		lastStream = point.Stream
		breakPending = breakPending || point.BreakBefore
		res, err := statement.ExecContext(ctx, user.ID, prefix+point.Stream, point.Time, point.Lat, point.Lon, false, breakPending)
		if err != nil {
			serverError(w, err)
			return
		}
		if count, _ := res.RowsAffected(); count == 0 {
			summary.Duplicates++
		} else {
			summary.Added++
			breakPending = false
		}
	}
	if err := tx.Commit(); err != nil {
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, summary)
}

func parseGPX(body io.Reader) ([]fix, int, error) {
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

func parseCSV(body io.Reader) ([]fix, int, error) {
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
