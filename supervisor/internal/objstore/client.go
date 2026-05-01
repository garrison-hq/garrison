package objstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client is the M5.4 MinIO wrapper. Constructed once at supervisor
// startup, used by the dashboardapi /api/objstore/company-md handler.
type Client struct {
	mc        *minio.Client
	bucket    string
	companyID string
	logger    *slog.Logger
}

// Config bundles the env-var-derived settings needed to construct a
// Client. Constructed by cmd/supervisor/main.go from internal/config.
type Config struct {
	Endpoint  string // e.g. "garrison-minio:9000"
	UseTLS    bool
	AccessKey string
	SecretKey string
	Bucket    string // e.g. "garrison-company"
	CompanyID string // resolved once at startup from companies table
}

// New constructs a Client. Does NOT call MinIO. Use BootstrapBucket to
// verify reachability + ensure the bucket exists.
func New(cfg Config, logger *slog.Logger) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("objstore: endpoint required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("objstore: access key + secret key required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("objstore: bucket required")
	}
	if cfg.CompanyID == "" {
		return nil, errors.New("objstore: companyID required")
	}
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("objstore: minio.New: %w", err)
	}
	return &Client{
		mc:        mc,
		bucket:    cfg.Bucket,
		companyID: cfg.CompanyID,
		logger:    logger,
	}, nil
}

// BootstrapBucket runs at supervisor startup BEFORE the event-bus
// listener. Idempotent (BucketExists → MakeBucket if false). Logs the
// outcome at info level. Returns ErrMinIOUnreachable on net failure
// so the supervisor exits ExitFailure (mirrors M2.2 mempalace
// bootstrap fail-closed pattern).
func (c *Client) BootstrapBucket(ctx context.Context) error {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("%w: BucketExists: %v", ErrMinIOUnreachable, err)
	}
	if exists {
		c.logger.Info("objstore: bucket exists", "name", c.bucket)
		return nil
	}
	if err := c.mc.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("%w: MakeBucket: %v", ErrMinIOUnreachable, err)
	}
	c.logger.Info("objstore: bucket created", "name", c.bucket)
	return nil
}

// objectKey returns the S3 key for the company.md object.
func (c *Client) objectKey() string {
	return c.companyID + "/company.md"
}

// GetCompanyMD fetches the current Company.md object. Returns
// (content, etag, nil) on hit; (nil, "", nil) on 404 (FR-624 empty-
// state); (nil, "", typed error) otherwise.
//
// minio-go's GetObject is lazy — it returns a handle without
// performing the HTTP request; the actual error surfaces on Stat() /
// Read(). NoSuchKey is checked at every error site below so the
// FR-624 empty-state path returns (nil, "", nil) cleanly instead of
// being misclassified as ErrMinIOUnreachable by the catch-all
// classifier.
func (c *Client) GetCompanyMD(ctx context.Context) ([]byte, string, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, c.objectKey(), minio.GetObjectOptions{})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, "", nil
		}
		return nil, "", classifyMinIOErr(err)
	}
	defer obj.Close()
	stat, err := obj.Stat()
	if err != nil {
		if isNoSuchKey(err) {
			return nil, "", nil
		}
		return nil, "", classifyMinIOErr(err)
	}
	content, err := io.ReadAll(obj)
	if err != nil {
		if isNoSuchKey(err) {
			return nil, "", nil
		}
		return nil, "", classifyMinIOErr(err)
	}
	return content, stat.ETag, nil
}

// PutCompanyMD writes the object after running size-cap + leak-scan
// pre-checks. ifMatchEtag is validated against the current object's
// ETag; on mismatch returns ErrStale. Returns the new ETag on success.
//
// ifMatchEtag may be empty when the object does not yet exist (first
// save against the empty-state UX path); the supervisor then confirms
// the object is still missing before writing, so two parallel "first
// saves" don't silently clobber each other.
func (c *Client) PutCompanyMD(ctx context.Context, content []byte, ifMatchEtag string) (string, error) {
	if err := CheckSize(content); err != nil {
		return "", err
	}
	if err := Scan(content); err != nil {
		return "", err
	}

	// ETag pre-check: stat + compare. Race window is bounded by
	// single-operator constraint (Constitution X); a future
	// milestone may switch to server-side If-Match if the race
	// surfaces.
	stat, err := c.mc.StatObject(ctx, c.bucket, c.objectKey(), minio.StatObjectOptions{})
	if err != nil {
		// 404 is OK if ifMatchEtag is empty (first save).
		if isNoSuchKey(err) {
			if ifMatchEtag != "" {
				return "", ErrStale
			}
		} else {
			return "", classifyMinIOErr(err)
		}
	} else if stat.ETag != ifMatchEtag {
		return "", ErrStale
	}

	info, err := c.mc.PutObject(ctx, c.bucket, c.objectKey(), bytes.NewReader(content), int64(len(content)), minio.PutObjectOptions{
		ContentType: "text/markdown",
	})
	if err != nil {
		return "", classifyMinIOErr(err)
	}
	return info.ETag, nil
}

// classifyMinIOErr maps a minio-go error to the typed sentinel set.
// Unknown errors collapse to ErrMinIOUnreachable so the operator's
// surface always sees one of the named kinds.
func classifyMinIOErr(err error) error {
	if err == nil {
		return nil
	}
	if isNoSuchKey(err) {
		// Caller-specific behaviour — Get translates to (nil,"",nil),
		// Put pre-check translates to ErrStale-or-OK. classifyMinIOErr
		// only hits here when the caller explicitly didn't pre-check.
		return ErrMinIOUnreachable
	}
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		switch er.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized:
			return fmt.Errorf("%w: %s", ErrMinIOAuthFailed, er.Message)
		case http.StatusPreconditionFailed:
			return ErrStale
		}
	}
	return fmt.Errorf("%w: %v", ErrMinIOUnreachable, err)
}

// isNoSuchKey returns true for the MinIO 404-equivalent error.
func isNoSuchKey(err error) bool {
	var er minio.ErrorResponse
	if errors.As(err, &er) {
		return er.Code == "NoSuchKey" || er.StatusCode == http.StatusNotFound
	}
	return false
}
