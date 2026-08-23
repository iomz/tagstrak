package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/iomz/tagstrak/v2/internal/inventory"
)

func newHandler() *Handler {
	return NewHandler(inventory.NewService(inventory.NewStore(), "", inventory.DefaultLimits()))
}

func newRouter(handler *Handler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/tags", handler.PostTag)
	router.DELETE("/tags", handler.DeleteTag)
	router.GET("/tags", handler.GetTags)
	return router
}

func request(router *gin.Engine, method string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/tags", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestHandlerUpsertListAndDelete(t *testing.T) {
	router := newRouter(newHandler())
	response := request(router, http.MethodPost, `[{"epc":"3000"}]`)
	if response.Code != http.StatusOK {
		t.Fatalf("POST status = %d: %s", response.Code, response.Body.String())
	}
	var result map[string]int
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["inserted"] != 1 || result["updated"] != 0 {
		t.Fatalf("POST result = %#v", result)
	}

	response = request(router, http.MethodPost, `[{"epc":"3000"}]`)
	if response.Code != http.StatusOK {
		t.Fatalf("upsert status = %d", response.Code)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["inserted"] != 0 || result["updated"] != 1 {
		t.Fatalf("upsert result = %#v", result)
	}

	response = request(router, http.MethodGet, "")
	if response.Code != http.StatusOK || response.Body.String() != `[{"epc":"3000"}]` {
		t.Fatalf("GET = %d %s", response.Code, response.Body.String())
	}
	response = request(router, http.MethodDelete, `[{"epc":"3000"}]`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d", response.Code)
	}
}

func TestHandlerRejectsInvalidOrMissingTags(t *testing.T) {
	router := newRouter(newHandler())
	if response := request(router, http.MethodPost, `[{"epc":"odd"}]`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid POST status = %d", response.Code)
	}
	if response := request(router, http.MethodDelete, `[{"epc":"3000"}]`); response.Code != http.StatusNotFound {
		t.Fatalf("missing DELETE status = %d", response.Code)
	}
}
