package main

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const gapMillis = int64(10 * time.Minute / time.Millisecond)

type fix struct {
	ID          int64   `json:"id"`
	Time        int64   `json:"time"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Stream      string  `json:"stream"`
	Revision    int     `json:"revision"`
	Corrected   bool    `json:"corrected"`
	Estimated   bool    `json:"estimated"`
	BreakBefore bool    `json:"breakBefore"`
}

type coordinates struct {
	Lat *float64 `json:"lat"`
	Lon *float64 `json:"lon"`
}

func (c coordinates) valid() bool {
	return c.Lat != nil && c.Lon != nil && validCoordinates(*c.Lat, *c.Lon)
}

func validCoordinates(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func parseTimestamp(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if timestamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return timestamp.UnixMilli(), nil
	}
	for _, format := range []string{"2006-01-02T15:04:05.999Z0700", "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05Z0700"} {
		if timestamp, err := time.Parse(format, value); err == nil {
			return timestamp.UnixMilli(), nil
		}
	}
	return 0, errors.New("invalid recording time")
}

const insertFix = `INSERT INTO points (user_id, stream, recorded_at, lat, lon, estimated, break_before)
	VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`

func (a *app) upload(w http.ResponseWriter, r *http.Request) {
	scheme, key, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || len(key) != 43 {
		problem(w, http.StatusUnauthorized, "A GPSLogger API key is required")
		return
	}
	ctx := r.Context()
	keyHash := tokenHash(key)
	var device, owner int64
	var lastUsed sql.NullInt64
	err := a.reader().QueryRowContext(ctx, "SELECT id, user_id, last_used FROM device_keys WHERE hash = ? AND revoked IS NULL", keyHash).Scan(&device, &owner, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, http.StatusUnauthorized, "Invalid or revoked GPSLogger API key")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	var input struct {
		coordinates
		Time string `json:"time"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	ts, err := parseTimestamp(input.Time)
	if err != nil || !input.valid() {
		problem(w, http.StatusBadRequest, "Latitude, longitude and an ISO 8601 recording time with a timezone are required")
		return
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()
	var active bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM device_keys WHERE id = ? AND revoked IS NULL)", device).Scan(&active)
	if err != nil {
		serverError(w, err)
		return
	}
	if !active {
		problem(w, http.StatusUnauthorized, "Invalid or revoked GPSLogger API key")
		return
	}
	now := time.Now().Unix()
	if !lastUsed.Valid || now-lastUsed.Int64 >= 60 {
		if _, err = tx.ExecContext(ctx, "UPDATE device_keys SET last_used = ? WHERE id = ?", now, device); err != nil {
			serverError(w, err)
			return
		}
	}
	if _, err = tx.ExecContext(ctx, insertFix, owner, "device-"+strconv.FormatInt(device, 10), ts, *input.Lat, *input.Lon, false, false); err != nil {
		serverError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func timeRange(r *http.Request) (int64, int64, bool) {
	query := r.URL.Query()
	start, errStart := strconv.ParseInt(query.Get("from"), 10, 64)
	end, errEnd := strconv.ParseInt(query.Get("to"), 10, 64)
	return start, end, errStart == nil && errEnd == nil && start >= -62135596800000 && end <= 253402300799999 && start < end
}

const pointFields = "id, recorded_at, COALESCE(edited_lat, lat), COALESCE(edited_lon, lon), stream, revision, edited_lat IS NOT NULL, estimated, break_before"

type scanner interface {
	Scan(...any) error
}

func scanFix(row scanner) (fix, error) {
	var point fix
	err := row.Scan(&point.ID, &point.Time, &point.Lat, &point.Lon, &point.Stream, &point.Revision, &point.Corrected, &point.Estimated, &point.BreakBefore)
	return point, err
}

func isGap(previous, point fix) bool {
	return point.BreakBefore || (!point.Estimated && !previous.Estimated && point.Time-previous.Time > gapMillis)
}

func (a *app) getRoute(w http.ResponseWriter, r *http.Request, user account) {
	start, end, ok := timeRange(r)
	if !ok {
		problem(w, http.StatusBadRequest, "Choose a valid date range")
		return
	}
	detail := r.URL.Query().Get("detail") == "1"
	ctx := r.Context()
	tx, err := a.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()
	var count int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM points WHERE user_id = ? AND recorded_at >= ? AND recorded_at < ?", user.ID, start, end).Scan(&count); err != nil {
		serverError(w, err)
		return
	}
	const limit = 4000
	if detail && count > limit {
		problem(w, http.StatusUnprocessableEntity, "Choose a shorter time range to edit these points")
		return
	}
	stride := max(int64(1), (count+limit-1)/limit)
	rows, err := tx.QueryContext(ctx, "SELECT "+pointFields+" FROM points WHERE user_id = ? AND recorded_at >= ? AND recorded_at < ? ORDER BY stream, recorded_at, id", user.ID, start, end)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	points := make([]fix, 0, min(count, limit))
	var last fix
	var latest *fix
	var index int64
	var distance float64
	appendPoint := func(point fix) {
		if len(points) == 0 || points[len(points)-1].ID != point.ID {
			points = append(points, point)
			if len(points) == limit*3 {
				points = trimOverview(points, limit*2)
			}
		}
	}
	for rows.Next() {
		point, err := scanFix(rows)
		if err != nil {
			serverError(w, err)
			return
		}
		if latest == nil || point.Time > latest.Time || point.Time == latest.Time && point.ID > latest.ID {
			current := point
			latest = &current
		}
		newStream := last.ID == 0 || point.Stream != last.Stream
		point.BreakBefore = !newStream && isGap(last, point)
		if !newStream && !point.BreakBefore {
			distance += distanceMeters(last.Lat, last.Lon, point.Lat, point.Lon)
		}
		if newStream || point.BreakBefore {
			if last.ID != 0 {
				appendPoint(last)
			}
			index = 0
		}
		if index%stride == 0 || point.Estimated {
			appendPoint(point)
		}
		last = point
		index++
	}
	if err := rows.Err(); err != nil {
		serverError(w, err)
		return
	}
	if last.ID != 0 {
		appendPoint(last)
	}
	points = trimOverview(points, limit)
	if err := tx.Commit(); err != nil {
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"points": points, "latest": latest, "count": count, "distance": math.Round(distance)})
}

// Carry breaks forward when decimation removes the original segment boundary.
func trimOverview(points []fix, limit int) []fix {
	if len(points) <= limit {
		return points
	}
	write := 1
	broken := false
	for read := 1; read < len(points); read++ {
		point := points[read]
		broken = broken || point.BreakBefore
		if read != write*(len(points)-1)/(limit-1) {
			continue
		}
		point.BreakBefore = broken && points[write-1].Stream == point.Stream
		points[write] = point
		broken = false
		write++
	}
	clear(points[limit:])
	return points[:limit]
}

func (a *app) movePoint(w http.ResponseWriter, r *http.Request, user account) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid point ID")
		return
	}
	var input struct {
		coordinates
		Revision int
		Revert   bool
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !input.Revert && !input.valid() {
		problem(w, http.StatusBadRequest, "Enter valid coordinates")
		return
	}
	if input.Revert {
		input.Lat, input.Lon = nil, nil
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE points SET edited_lat = ?, edited_lon = ?, revision = revision + 1
		WHERE id = ? AND user_id = ? AND revision = ?`, input.Lat, input.Lon, id, user.ID, input.Revision)
	if err != nil {
		serverError(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		problem(w, http.StatusConflict, "Point unavailable or changed. Refresh the route and try again.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) deletePoint(w http.ResponseWriter, r *http.Request, user account) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid point ID")
		return
	}
	revision, err := strconv.Atoi(r.URL.Query().Get("revision"))
	if err != nil {
		problem(w, http.StatusBadRequest, "Point revision is required")
		return
	}
	result, err := a.db.ExecContext(r.Context(), "DELETE FROM points WHERE id = ? AND user_id = ? AND revision = ?", id, user.ID, revision)
	if err != nil {
		serverError(w, err)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		problem(w, http.StatusConflict, "Point unavailable or changed. Refresh the route and try again.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) addPoints(w http.ResponseWriter, r *http.Request, user account) {
	var input struct {
		After  int64
		Before int64
		Points []struct {
			coordinates
			Time int64
		}
	}
	if !readJSON(w, r, &input) {
		return
	}
	if len(input.Points) == 0 || len(input.Points) > 100 {
		problem(w, http.StatusBadRequest, "Add between 1 and 100 waypoints")
		return
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()
	read := func(id int64) (fix, error) {
		return scanFix(tx.QueryRowContext(ctx, "SELECT "+pointFields+" FROM points WHERE id = ? AND user_id = ?", id, user.ID))
	}
	left, leftErr := read(input.After)
	right, rightErr := read(input.Before)
	if leftErr != nil && !errors.Is(leftErr, sql.ErrNoRows) {
		serverError(w, leftErr)
		return
	}
	if rightErr != nil && !errors.Is(rightErr, sql.ErrNoRows) {
		serverError(w, rightErr)
		return
	}
	if leftErr != nil || rightErr != nil || left.Stream != right.Stream || left.Time >= right.Time {
		problem(w, http.StatusConflict, "Select two consecutive points in the same route")
		return
	}
	var overlap bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM points WHERE user_id = ? AND stream = ?
		AND (recorded_at, id) > (?, ?) AND (recorded_at, id) < (?, ?))`,
		user.ID, left.Stream, left.Time, left.ID, right.Time, right.ID).Scan(&overlap)
	if err != nil {
		serverError(w, err)
		return
	}
	if overlap {
		problem(w, http.StatusConflict, "New points appeared in this section. Refresh the route and try again.")
		return
	}
	previousTime := left.Time
	for _, point := range input.Points {
		if !point.valid() || point.Time <= previousTime || point.Time >= right.Time {
			problem(w, http.StatusBadRequest, "Waypoints must have valid coordinates and increasing times between the two fixes")
			return
		}
		_, err := tx.ExecContext(ctx, insertFix, user.ID, left.Stream, point.Time, *point.Lat, *point.Lon, true, false)
		if err != nil {
			serverError(w, err)
			return
		}
		previousTime = point.Time
	}
	if right.BreakBefore {
		if _, err := tx.ExecContext(ctx, "UPDATE points SET break_before = 0 WHERE id = ? AND user_id = ?", right.ID, user.ID); err != nil {
			serverError(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func distanceMeters(lat, lon, otherLat, otherLon float64) float64 {
	const rad = math.Pi / 180
	a := math.Pow(math.Sin((otherLat-lat)*rad/2), 2) +
		math.Cos(lat*rad)*math.Cos(otherLat*rad)*math.Pow(math.Sin((otherLon-lon)*rad/2), 2)
	return 6371000 * 2 * math.Asin(math.Sqrt(min(a, 1)))
}
