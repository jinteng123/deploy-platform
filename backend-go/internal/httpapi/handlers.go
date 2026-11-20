package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/service"

	"github.com/gin-gonic/gin"
)

func isNotFound(err error) bool { return errors.Is(err, service.ErrNotFound) }

// ctxOpenAPIErr is set on every /openapi/v1 request so fail() emits the
// documented {error: msg} envelope; the session API uses {detail: msg}.
const ctxOpenAPIErr = "kb.openapi.err"

func fail(c *gin.Context, code int, msg string) {
	key := "detail"
	if c.GetBool(ctxOpenAPIErr) {
		key = "error"
	}
	c.JSON(code, gin.H{key: msg})
}

func bindJSON(c *gin.Context, v any) bool {
	if err := c.ShouldBindJSON(v); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func nonEmpty(s string) bool { return strings.TrimSpace(s) != "" }

// trimPtr normalizes an optional string: trimmed, empty becomes nil.
func trimPtr(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return nil
	}
	return &s
}

// requireStrings reports 400 when any value is empty after trimming.
func requireStrings(c *gin.Context, fields map[string]string) bool {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !nonEmpty(fields[name]) {
			fail(c, http.StatusBadRequest, fmt.Sprintf("%s 不能为空", name))
			return false
		}
	}
	return true
}

// pathInt parses a required integer path parameter; failure → 400.
func pathInt(c *gin.Context, name string) (int, bool) {
	id, err := strconv.Atoi(c.Param(name))
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, fmt.Sprintf("非法 %s", name))
		return 0, false
	}
	return id, true
}

// pathInt64 parses a required int64 path parameter; failure → 400.
func pathInt64(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		fail(c, http.StatusBadRequest, fmt.Sprintf("非法 %s", name))
		return 0, false
	}
	return id, true
}

// queryIntOpt parses an optional integer query param; failure → 400.
func queryIntOpt(c *gin.Context, key string) (int, bool) {
	raw := c.Query(key)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		fail(c, http.StatusBadRequest, fmt.Sprintf("非法参数 %s", key))
		return 0, false
	}
	return v, true
}

// queryIntRange parses an integer query param with default and [min,max].
func queryIntRange(c *gin.Context, key string, def, min, max int) (int, bool) {
	raw := c.Query(key)
	if raw == "" {
		return def, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		fail(c, http.StatusBadRequest, fmt.Sprintf("参数 %s 必须在 %d-%d 之间", key, min, max))
		return 0, false
	}
	return v, true
}

// fmtTime renders time like Python's naive datetime.isoformat().
func fmtTime(t time.Time) string { return t.Format("2006-01-02T15:04:05") }

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := fmtTime(*t)
	return &s
}
