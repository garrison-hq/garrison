package dashboardapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
)

// ObjstoreClient is the seam newObjstoreHandler uses for tests. The
// production *objstore.Client satisfies this interface; tests
// substitute a fake without standing up MinIO.
type ObjstoreClient interface {
	GetCompanyMD(ctx context.Context) ([]byte, string, error)
	PutCompanyMD(ctx context.Context, content []byte, ifMatchEtag string) (string, error)
}

// getResponse is the Get success-path body. ETag is rendered as a JSON
// string on hit; on FR-624 empty-state (no object yet) it's a JSON
// null pointer + empty content.
type getResponse struct {
	Content string  `json:"content"`
	ETag    *string `json:"etag"`
}

// putResponse echoes the new ETag after a successful save.
type putResponse struct {
	Content string `json:"content"`
	ETag    string `json:"etag"`
}

// newObjstoreHandler returns the GET / PUT handler routed at
// /api/objstore/company-md. The handler is method-multiplexing — the
// caller registers the same handler against a single mux entry per
// FR-622.
func newObjstoreHandler(client ObjstoreClient, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleObjstoreGet(w, r, client, logger)
		case http.MethodPut:
			handleObjstorePut(w, r, client, logger)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeErrorResponse(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "", "", logger)
		}
	})
}

func handleObjstoreGet(w http.ResponseWriter, r *http.Request, client ObjstoreClient, logger *slog.Logger) {
	content, etag, err := client.GetCompanyMD(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, objstore.ErrMinIOAuthFailed):
			writeErrorResponse(w, http.StatusInternalServerError, "MinIOAuthFailed", "", "", logger)
		case errors.Is(err, objstore.ErrMinIOUnreachable):
			writeErrorResponse(w, http.StatusServiceUnavailable, "MinIOUnreachable", "", "", logger)
		default:
			if logger != nil {
				logger.Error("dashboardapi: GetCompanyMD failed", "err", err)
			}
			writeErrorResponse(w, http.StatusInternalServerError, "InternalError", "", "", logger)
		}
		return
	}

	body := getResponse{Content: string(content)}
	// FR-624: empty-state returns content="" + etag=null. Otherwise
	// serialise the etag as a JSON string.
	if etag != "" {
		e := etag
		body.ETag = &e
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(body); err != nil && logger != nil {
		logger.Error("dashboardapi: write GET body", "err", err)
	}
}

func handleObjstorePut(w http.ResponseWriter, r *http.Request, client ObjstoreClient, logger *slog.Logger) {
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeErrorResponse(w, http.StatusBadRequest, "MissingIfMatch", "If-Match header required", "", logger)
		return
	}

	// Bound the read to MaxCompanyMDBytes + 1 so Content-Length spoofing
	// doesn't lead the supervisor into reading an unbounded body. The
	// extra byte lets PutCompanyMD's CheckSize see "exactly over the
	// limit" and reject with ErrTooLarge instead of misclassifying.
	limited := io.LimitReader(r.Body, int64(objstore.MaxCompanyMDBytes)+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		if logger != nil {
			logger.Error("dashboardapi: read PUT body", "err", err)
		}
		writeErrorResponse(w, http.StatusBadRequest, "ReadBodyFailed", "", "", logger)
		return
	}

	newEtag, err := client.PutCompanyMD(r.Context(), content, ifMatch)
	if err != nil {
		var leak *objstore.LeakScanError
		if errors.As(err, &leak) {
			// Rule 1: surface the category, NEVER the matched substring.
			writeErrorResponse(w, http.StatusUnprocessableEntity, "LeakScanFailed", "", string(leak.Category), logger)
			return
		}
		switch {
		case errors.Is(err, objstore.ErrTooLarge):
			writeErrorResponse(w, http.StatusRequestEntityTooLarge, "TooLarge", "", "", logger)
		case errors.Is(err, objstore.ErrStale):
			writeErrorResponse(w, http.StatusPreconditionFailed, "Stale", "", "", logger)
		case errors.Is(err, objstore.ErrMinIOAuthFailed):
			writeErrorResponse(w, http.StatusInternalServerError, "MinIOAuthFailed", "", "", logger)
		case errors.Is(err, objstore.ErrMinIOUnreachable):
			writeErrorResponse(w, http.StatusServiceUnavailable, "MinIOUnreachable", "", "", logger)
		default:
			if logger != nil {
				logger.Error("dashboardapi: PutCompanyMD failed", "err", err)
			}
			writeErrorResponse(w, http.StatusInternalServerError, "InternalError", "", "", logger)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(putResponse{Content: string(content), ETag: newEtag}); err != nil && logger != nil {
		logger.Error("dashboardapi: write PUT body", "err", err)
	}
}
