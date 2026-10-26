package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"crud-api/internal/store"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type API struct {
	store    *store.Store
	logger   *slog.Logger
	mux      *http.ServeMux
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func New(database *store.Store, logger *slog.Logger) *API {
	registry := prometheus.NewRegistry()
	server := &API{
		store:  database,
		logger: logger,
		mux:    http.NewServeMux(),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "HTTP requests by method, route, and status.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	registry.MustRegister(server.requests, server.duration, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	server.mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) {
		server.writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	server.mux.HandleFunc("GET /readyz", server.ready)
	server.mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	server.mux.HandleFunc("POST /api/v1/items", server.create)
	server.mux.HandleFunc("GET /api/v1/items", server.list)
	server.mux.HandleFunc("GET /api/v1/items/{id}", server.get)
	server.mux.HandleFunc("PUT /api/v1/items/{id}", server.update)
	server.mux.HandleFunc("DELETE /api/v1/items/{id}", server.delete)
	return server
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *responseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
		writer.ResponseWriter.WriteHeader(status)
	}
}

func (writer *responseWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(body)
}

func (server *API) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	response := &responseWriter{ResponseWriter: writer}
	response.Header().Set("X-Content-Type-Options", "nosniff")
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	request = request.WithContext(ctx)
	defer func() {
		if recovered := recover(); recovered != nil {
			server.logger.ErrorContext(ctx, "request panic", "error", fmt.Sprint(recovered))
			if response.status == 0 {
				server.fail(response, http.StatusInternalServerError, "internal server error")
			}
		}
		if response.status == 0 {
			response.WriteHeader(http.StatusOK)
		}
		route := request.Pattern
		if route == "" {
			route = "unmatched"
		}
		method := request.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS":
		default:
			method = "OTHER"
		}
		elapsed := time.Since(started).Seconds()
		server.requests.WithLabelValues(method, route, strconv.Itoa(response.status)).Inc()
		server.duration.WithLabelValues(method, route).Observe(elapsed)
		server.logger.InfoContext(ctx, "http request", "method", method, "route", route, "status", response.status, "duration_seconds", elapsed)
	}()
	server.mux.ServeHTTP(response, request)
}

func (server *API) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		server.logger.Warn("write JSON response", "error", err)
	}
}

func (server *API) fail(writer http.ResponseWriter, status int, message string) {
	server.writeJSON(writer, status, map[string]string{"error": message})
}

func (server *API) storageError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		server.fail(writer, http.StatusNotFound, "item not found")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		server.logger.WarnContext(request.Context(), "database request interrupted", "error", err)
		server.fail(writer, http.StatusServiceUnavailable, "request interrupted or timed out")
	default:
		server.logger.ErrorContext(request.Context(), "database operation failed", "error", err)
		server.fail(writer, http.StatusInternalServerError, "internal server error")
	}
}

func (server *API) ready(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), time.Second)
	defer cancel()
	if err := server.store.Ping(ctx); err != nil {
		server.logger.WarnContext(ctx, "readiness check failed", "error", err)
		server.fail(writer, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	server.writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (server *API) input(writer http.ResponseWriter, request *http.Request) (store.Input, bool) {
	var input *store.Input
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		server.fail(writer, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return store.Input{}, false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&input)
	if err == nil {
		var extra any
		if trailingErr := decoder.Decode(&extra); !errors.Is(trailingErr, io.EOF) {
			if trailingErr != nil {
				err = trailingErr
			} else {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil || input == nil {
		var sizeError *http.MaxBytesError
		if errors.As(err, &sizeError) {
			server.fail(writer, http.StatusRequestEntityTooLarge, "body exceeds 64 KiB")
		} else {
			server.fail(writer, http.StatusBadRequest, "body must be one valid JSON object with name, description, and completed fields only")
		}
		return store.Input{}, false
	}
	input.Name = strings.TrimSpace(input.Name)
	if !utf8.ValidString(input.Name) || len(input.Name) == 0 || utf8.RuneCountInString(input.Name) > 200 {
		server.fail(writer, http.StatusBadRequest, "name must contain 1 to 200 characters")
		return store.Input{}, false
	}
	if !utf8.ValidString(input.Description) || utf8.RuneCountInString(input.Description) > 10000 {
		server.fail(writer, http.StatusBadRequest, "description must contain at most 10000 characters")
		return store.Input{}, false
	}
	return *input, true
}

func (server *API) itemID(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(request.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		server.fail(writer, http.StatusBadRequest, "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func (server *API) create(writer http.ResponseWriter, request *http.Request) {
	input, valid := server.input(writer, request)
	if !valid {
		return
	}
	item, err := server.store.Create(request.Context(), input)
	if err != nil {
		server.storageError(writer, request, err)
		return
	}
	writer.Header().Set("Location", fmt.Sprintf("/api/v1/items/%d", item.ID))
	server.writeJSON(writer, http.StatusCreated, item)
}

func (server *API) get(writer http.ResponseWriter, request *http.Request) {
	id, valid := server.itemID(writer, request)
	if !valid {
		return
	}
	item, err := server.store.Get(request.Context(), id)
	if err != nil {
		server.storageError(writer, request, err)
		return
	}
	server.writeJSON(writer, http.StatusOK, item)
}

func (server *API) list(writer http.ResponseWriter, request *http.Request) {
	limit, offset := 50, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if values, exists := request.URL.Query()[key]; exists {
			if len(values) != 1 {
				server.fail(writer, http.StatusBadRequest, "pagination parameters must not be repeated")
				return
			}
			value, err := strconv.Atoi(values[0])
			if err != nil || (key == "limit" && (value < 1 || value > 100)) || (key == "offset" && value < 0) {
				server.fail(writer, http.StatusBadRequest, "limit must be 1 to 100 and offset must be nonnegative")
				return
			}
			*target = value
		}
	}
	items, err := server.store.List(request.Context(), limit, offset)
	if err != nil {
		server.storageError(writer, request, err)
		return
	}
	server.writeJSON(writer, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (server *API) update(writer http.ResponseWriter, request *http.Request) {
	id, valid := server.itemID(writer, request)
	if !valid {
		return
	}
	input, valid := server.input(writer, request)
	if !valid {
		return
	}
	item, err := server.store.Update(request.Context(), id, input)
	if err != nil {
		server.storageError(writer, request, err)
		return
	}
	server.writeJSON(writer, http.StatusOK, item)
}

func (server *API) delete(writer http.ResponseWriter, request *http.Request) {
	id, valid := server.itemID(writer, request)
	if !valid {
		return
	}
	if err := server.store.Delete(request.Context(), id); err != nil {
		server.storageError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
