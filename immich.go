package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var assetID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (a *app) immichStatus(w http.ResponseWriter, r *http.Request, user account) {
	var hasKey bool
	if err := a.reader().QueryRowContext(r.Context(), "SELECT immich_key IS NOT NULL FROM users WHERE id = ?", user.ID).Scan(&hasKey); err != nil {
		serverError(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"available": a.immichURL != "", "connected": a.immichURL != "" && hasKey, "url": a.immichURL})
}

func (a *app) saveImmich(w http.ResponseWriter, r *http.Request, user account) {
	if a.immichURL == "" {
		problem(w, http.StatusServiceUnavailable, "The server operator has not configured Immich")
		return
	}
	var input struct{ Key string }
	if !readJSON(w, r, &input) {
		return
	}
	input.Key = strings.TrimSpace(input.Key)
	if input.Key == "" || len(input.Key) > 512 || strings.ContainsAny(input.Key, "\r\n") {
		problem(w, http.StatusBadRequest, "Enter a valid Immich API key")
		return
	}
	assets, _, err := a.searchPhotos(r.Context(), input.Key, 0, time.Now().AddDate(1, 0, 0).UnixMilli(), 1, 1)
	if err != nil {
		problem(w, http.StatusBadGateway, "Immich could not search photos. Check the server URL and the asset.read permission.")
		return
	}
	if len(assets) != 0 && assetID.MatchString(assets[0].ID) {
		res, err := a.immichRequest(r.Context(), input.Key, http.MethodGet, "/assets/"+assets[0].ID+"/thumbnail?size=thumbnail", nil)
		if err != nil {
			problem(w, http.StatusBadGateway, "Immich could not load a thumbnail. Check the asset.view permission.")
			return
		}
		res.Body.Close()
	}
	nonce := randBytes(a.secrets.NonceSize())
	ciphertext := a.secrets.Seal(nonce, nonce, []byte(input.Key), a.immichBinding(user))
	if _, err := a.db.ExecContext(r.Context(), "UPDATE users SET immich_key = ? WHERE id = ?", ciphertext, user.ID); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) deleteImmich(w http.ResponseWriter, r *http.Request, user account) {
	if _, err := a.db.ExecContext(r.Context(), "UPDATE users SET immich_key = NULL WHERE id = ?", user.ID); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *app) immichBinding(user account) []byte {
	return []byte(strconv.FormatInt(user.ID, 10) + " " + a.immichURL)
}

func (a *app) immichKey(ctx context.Context, user account) (string, error) {
	if a.secrets == nil {
		return "", nil
	}
	var encrypted []byte
	if err := a.reader().QueryRowContext(ctx, "SELECT immich_key FROM users WHERE id = ?", user.ID).Scan(&encrypted); err != nil {
		return "", err
	}
	if len(encrypted) == 0 {
		return "", nil
	}
	size := a.secrets.NonceSize()
	if len(encrypted) < size {
		return "", errors.New("invalid stored Immich credential")
	}
	key, err := a.secrets.Open(nil, encrypted[:size], encrypted[size:], a.immichBinding(user))
	return string(key), err
}

func (a *app) immichRequest(ctx context.Context, key, method, endpoint string, payload []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, a.immichURL+"/api"+endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("x-api-key", key)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	res, err := a.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("Immich responded with HTTP %d", res.StatusCode)
	}
	return res, nil
}

type immichAsset struct {
	ID        string `json:"id"`
	CreatedAt string `json:"fileCreatedAt"`
	Exif      struct {
		Lat *float64 `json:"latitude"`
		Lon *float64 `json:"longitude"`
	} `json:"exifInfo"`
}

func (a *app) searchPhotos(ctx context.Context, key string, from, to int64, page, size int) ([]immichAsset, bool, error) {
	payload, err := json.Marshal(map[string]any{
		"type": "IMAGE", "withExif": true, "page": page, "size": size,
		"takenAfter": time.UnixMilli(from).UTC(), "takenBefore": time.UnixMilli(to - 1).UTC(),
	})
	if err != nil {
		return nil, false, err
	}
	res, err := a.immichRequest(ctx, key, http.MethodPost, "/search/metadata", payload)
	if err != nil {
		return nil, false, err
	}
	defer res.Body.Close()
	var result struct {
		Assets struct {
			Items    []immichAsset `json:"items"`
			NextPage *string       `json:"nextPage"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&result); err != nil {
		return nil, false, err
	}
	return result.Assets.Items, result.Assets.NextPage != nil && *result.Assets.NextPage != "", nil
}

type photo struct {
	ID        string  `json:"id"`
	Time      int64   `json:"time"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Estimated bool    `json:"estimated"`
}

const (
	untaggedPhotoWindow = int64(5 * time.Minute / time.Millisecond)
	geotaggedPhotoWindow = untaggedPhotoWindow * 3
)

func (a *app) loadPointsWindow(ctx context.Context, userID, from, to int64) ([]fix, error) {
	rows, err := a.reader().QueryContext(ctx, "SELECT "+pointFields+" FROM points WHERE user_id = ? AND recorded_at BETWEEN ? AND ? ORDER BY recorded_at, id",
		userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []fix
	for rows.Next() {
		point, err := scanFix(rows)
		if err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func (a *app) photos(w http.ResponseWriter, r *http.Request, user account) {
	from, to, ok := timeRange(r)
	if !ok {
		problem(w, http.StatusBadRequest, "Choose a valid date range")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 28*time.Second)
	defer cancel()
	key, err := a.immichKey(ctx, user)
	if err != nil {
		problem(w, http.StatusBadGateway, "Reconnect your Immich API key in settings")
		return
	}
	matches := make([]photo, 0)
	var more bool
	if key != "" {
		points, err := a.loadPointsWindow(ctx, user.ID, from-geotaggedPhotoWindow, to+geotaggedPhotoWindow)
		if err != nil {
			serverError(w, err)
			return
		}
		if len(points) > 0 {
			for page := 1; page <= 5; page++ {
				assets, next, err := a.searchPhotos(ctx, key, from, to, page, 200)
				if err != nil {
					problem(w, http.StatusBadGateway, "Immich photos are temporarily unavailable")
					return
				}
				for _, asset := range assets {
					ts, err := parseTimestamp(asset.CreatedAt)
					if err != nil || ts < from || ts >= to || !assetID.MatchString(asset.ID) {
						continue
					}
					if err := ctx.Err(); err != nil {
						problem(w, http.StatusGatewayTimeout, "Photo matching timed out. Try a shorter range.")
						return
					}
					if match, found := matchPhotoFromPoints(points, asset, ts); found {
						matches = append(matches, match)
					}
				}
				more = next
				if !next {
					break
				}
			}
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"photos": matches, "partial": more})
}

func matchPhotoFromPoints(points []fix, asset immichAsset, at int64) (photo, bool) {
	result := photo{ID: asset.ID, Time: at}
	geotagged := asset.Exif.Lat != nil && asset.Exif.Lon != nil && validCoordinates(*asset.Exif.Lat, *asset.Exif.Lon)
	window := untaggedPhotoWindow
	if geotagged {
		window = geotaggedPhotoWindow
	}
	startTime, endTime := at-window, at+window
	startIdx := sort.Search(len(points), func(i int) bool { return points[i].Time >= startTime })
	previous := make(map[string]fix)
	best := window + 1
	for i := startIdx; i < len(points) && points[i].Time <= endTime; i++ {
		point := points[i]
		if geotagged {
			if distanceMeters(*asset.Exif.Lat, *asset.Exif.Lon, point.Lat, point.Lon) <= 250 {
				result.Lat, result.Lon = *asset.Exif.Lat, *asset.Exif.Lon
				return result, true
			}
			continue
		}
		left, ok := previous[point.Stream]
		previous[point.Stream] = point
		if !ok || left.Time > at || point.Time < at || point.Time == left.Time || isGap(left, point) {
			continue
		}
		span := point.Time - left.Time
		if span >= best || distanceMeters(left.Lat, left.Lon, point.Lat, point.Lon) > 2000 {
			continue
		}
		fraction := float64(at-left.Time) / float64(span)
		deltaLon := math.Mod(point.Lon-left.Lon+540, 360) - 180
		result.Lat = left.Lat + fraction*(point.Lat-left.Lat)
		result.Lon = math.Mod(left.Lon+fraction*deltaLon+540, 360) - 180
		result.Estimated = true
		best = span
	}
	return result, result.Estimated
}

func (a *app) thumbnail(w http.ResponseWriter, r *http.Request, user account) {
	id := r.PathValue("id")
	if !assetID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	key, err := a.immichKey(r.Context(), user)
	if err != nil || key == "" {
		http.NotFound(w, r)
		return
	}
	res, err := a.immichRequest(r.Context(), key, http.MethodGet, "/assets/"+id+"/thumbnail?size=thumbnail", nil)
	if err != nil {
		problem(w, http.StatusBadGateway, "Immich thumbnail unavailable")
		return
	}
	defer res.Body.Close()
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(res.Header.Get("Content-Type"), ";", 2)[0]))
	switch mediaType {
	case "image/jpeg", "image/png", "image/webp", "image/avif", "image/gif":
		w.Header().Set("Content-Type", mediaType)
	default:
		problem(w, http.StatusBadGateway, "Immich returned an unsupported thumbnail")
		return
	}
	if res.ContentLength > 5<<20 {
		problem(w, http.StatusBadGateway, "Immich returned a thumbnail that is too large")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Add("Vary", "Cookie")
	_, _ = io.Copy(w, io.LimitReader(res.Body, 5<<20))
}
