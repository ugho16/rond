// Copyright 2021 Mia srl
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rond-authz/rond/internal/config"
	"github.com/rond-authz/rond/internal/mocks"
	"github.com/rond-authz/rond/internal/mongoclient"
	"github.com/rond-authz/rond/internal/utils"
	"github.com/rond-authz/rond/openapi"
	"github.com/rond-authz/rond/types"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
	"gopkg.in/h2non/gock.v1"
)

func TestRoundTripErrors(t *testing.T) {
	logger, _ := test.NewNullLogger()
	envs := config.EnvironmentVariables{}

	defer gock.Off()

	t.Run("on unexpected status code from default RoundTrip, proxy error and do nothing", func(t *testing.T) {
		defer gock.Flush()

		responseBody := map[string]interface{}{"answer": float64(42)}
		gock.DisableNetworking()
		gock.New("http://example.com").
			Post("/some-api").
			Reply(http.StatusExpectationFailed). // 417
			JSON(responseBody)

		req := httptest.NewRequest(http.MethodPost, "http://example.com/some-api", nil)
		transport := &OPATransport{
			http.DefaultTransport,
			req.Context(),
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}

		resp, err := transport.RoundTrip(req)
		require.NoError(t, err, "unexpected error")
		require.Equal(t, http.StatusExpectationFailed, resp.StatusCode, "unexpected status code")

		bodyBytes, err := io.ReadAll(resp.Body)
		require.NoError(t, err, "unexpected error")

		actualResponseBody := make(map[string]interface{})
		err = json.Unmarshal(bodyBytes, &actualResponseBody)
		require.NoError(t, err, "unexpected error")

		require.Equal(t, responseBody, actualResponseBody)
	})
}

func TestIs2xx(t *testing.T) {
	require.True(t, is2XX(200))
	require.True(t, is2XX(201))
	require.False(t, is2XX(300))
	require.False(t, is2XX(199))
}

func TestOPATransportResponseWithError(t *testing.T) {
	envs := config.EnvironmentVariables{}
	logger, _ := test.NewNullLogger()

	req := httptest.NewRequest(http.MethodPost, "http://example.com/some-api", nil)

	transport := &OPATransport{
		http.DefaultTransport,
		req.Context(),
		logrus.NewEntry(logger),
		req,
		nil,
		nil,
		envs,
	}

	t.Run("generic business error message", func(t *testing.T) {
		resp := &http.Response{
			Body:          nil,
			ContentLength: 0,
			Header:        http.Header{},
		}

		transport.responseWithError(resp, fmt.Errorf("some error"), http.StatusInternalServerError)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

		bodyBytes, err := io.ReadAll(resp.Body)
		require.Nil(t, err)
		expectedBytes, err := json.Marshal(types.RequestError{
			StatusCode: http.StatusInternalServerError,
			Message:    utils.GENERIC_BUSINESS_ERROR_MESSAGE,
			Error:      "some error",
		})
		require.Nil(t, err)
		require.Equal(t, string(expectedBytes), string(bodyBytes))
		require.Equal(t, strconv.Itoa(len(expectedBytes)), resp.Header.Get("content-length"))
	})

	t.Run("permissions error message", func(t *testing.T) {
		resp := &http.Response{
			Body:          nil,
			ContentLength: 0,
			Header:        http.Header{},
		}

		transport.responseWithError(resp, fmt.Errorf("some error"), http.StatusForbidden)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)

		bodyBytes, err := io.ReadAll(resp.Body)
		require.Nil(t, err)
		expectedBytes, err := json.Marshal(types.RequestError{
			StatusCode: http.StatusForbidden,
			Message:    utils.NO_PERMISSIONS_ERROR_MESSAGE,
			Error:      "some error",
		})
		require.Nil(t, err)
		require.Equal(t, string(expectedBytes), string(bodyBytes))
		require.Equal(t, strconv.Itoa(len(expectedBytes)), resp.Header.Get("content-length"))
	})
}

func TestOPATransportRoundTrip(t *testing.T) {
	envs := config.EnvironmentVariables{
		UserIdHeader:         "useridheader",
		UserGroupsHeader:     "usergroupsheader",
		UserPropertiesHeader: "userpropertiesheader",
	}

	logger, _ := test.NewNullLogger()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/some-api", nil)

	t.Run("returns error on RoundTrip error", func(t *testing.T) {
		transport := &OPATransport{
			&MockRoundTrip{Error: fmt.Errorf("some error")},
			req.Context(),
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}

		_, err := transport.RoundTrip(req)
		require.Error(t, err, "some error")
	})

	t.Run("returns resp on non-2xx response", func(t *testing.T) {
		resp := &http.Response{
			StatusCode:    http.StatusInternalServerError,
			Body:          io.NopCloser(bytes.NewReader([]byte("original response"))),
			ContentLength: 0,
			Header:        http.Header{},
		}
		transport := &OPATransport{
			&MockRoundTrip{Response: resp},
			req.Context(),
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}

		updatedResp, err := transport.RoundTrip(req)
		require.Nil(t, err)
		bodyBytes, err := io.ReadAll(updatedResp.Body)
		require.Nil(t, err)
		require.Equal(t, "original response", string(bodyBytes))
	})

	t.Run("response read failure", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body: &MockReader{
				ReadError: fmt.Errorf("read error"),
			},
			ContentLength: 0,
			Header:        http.Header{},
		}
		transport := &OPATransport{
			&MockRoundTrip{Response: resp},
			req.Context(),
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}

		resp, err := transport.RoundTrip(req)
		require.Nil(t, resp)
		require.Error(t, err, "read error")
	})

	t.Run("response close failure", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body: &MockReader{
				ReadError:  io.EOF,
				CloseError: fmt.Errorf("close error"),
			},
			ContentLength: 0,
			Header:        http.Header{},
		}
		transport := &OPATransport{
			&MockRoundTrip{Response: resp},
			req.Context(),
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}

		resp, err := transport.RoundTrip(req)
		require.Nil(t, resp)
		require.Error(t, err, "close error")
	})

	t.Run("response as-is on empty response body", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Body: &MockReader{
				ReadError: io.EOF,
			},
			ContentLength: 0,
			Header:        http.Header{http.CanonicalHeaderKey("some"): []string{"content"}},
		}
		transport := &OPATransport{
			&MockRoundTrip{Response: resp},
			req.Context(),
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}

		resp, err := transport.RoundTrip(req)
		require.Nil(t, err)
		require.Equal(t, []string{"content"}, resp.Header[http.CanonicalHeaderKey("some")])
	})

	t.Run("failure on non-json response content-type", func(t *testing.T) {
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader([]byte("original response"))),
			ContentLength: 0,
			Header:        http.Header{"Content-Type": []string{"text/plain"}},
		}
		transport := &OPATransport{
			&MockRoundTrip{Response: resp},
			req.Context(),
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}

		resp, err := transport.RoundTrip(req)
		require.Nil(t, err)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		bodyBytes, err := io.ReadAll(resp.Body)
		require.Nil(t, err)
		require.True(t, strings.Contains(string(bodyBytes), "content-type is not application/json"))
	})

	t.Run("failure on non-json response even with json content-type", func(t *testing.T) {
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader([]byte("original response"))),
			ContentLength: 0,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
		}
		transport := &OPATransport{
			&MockRoundTrip{Response: resp},
			req.Context(),
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}

		resp, err := transport.RoundTrip(req)
		require.Nil(t, resp)
		require.Error(t, err, "response body is not valid")
	})

	t.Run("failure on get user bindings and roles", func(t *testing.T) {
		db := mocks.MongoClientMock{
			UserBindingsError: fmt.Errorf("fail from mongoclient"),
		}
		ctx := mongoclient.WithMongoClient(req.Context(), db)
		req := req.WithContext(ctx)
		req.Header.Set("useridheader", "userid")
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader([]byte(`{"hey":"there"}`))),
			ContentLength: 0,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
		}
		transport := &OPATransport{
			&MockRoundTrip{Response: resp},
			ctx,
			logrus.NewEntry(logger),
			req,
			nil,
			nil,
			envs,
		}
		resp, err := transport.RoundTrip(req)
		require.Nil(t, err)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		bodyBytes, err := io.ReadAll(resp.Body)
		require.Nil(t, err)
		require.True(t, strings.Contains(string(bodyBytes), "Error while retrieving user bindings"))
	})

	t.Run("failure on create rego input", func(t *testing.T) {
		req := req.Clone(req.Context())
		req.Header.Set("useridheader", "userid")
		req.Header.Set("groupsheader", "a,b,c")
		req.Header.Set("userpropertiesheader", "{}{}{}{{")
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader([]byte(`{"hey":"there"}`))),
			ContentLength: 0,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
		}
		transport := &OPATransport{
			&MockRoundTrip{Response: resp},
			req.Context(),
			logrus.NewEntry(logger),
			req,
			&openapi.RondConfig{
				ResponseFlow: openapi.ResponseFlow{PolicyName: "my_policy"},
			},
			PartialResultsEvaluators{"my_policy": {}},
			envs,
		}
		resp, err := transport.RoundTrip(req)
		require.Nil(t, err)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		bodyBytes, err := io.ReadAll(resp.Body)
		require.Nil(t, err)
		require.True(t, strings.Contains(string(bodyBytes), "user properties header is not valid"))
	})
}

type MockRoundTrip struct {
	Error    error
	Response *http.Response
}

func (m *MockRoundTrip) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	return m.Response, m.Error
}

type MockReader struct {
	ReadResult int
	ReadError  error
	CloseError error
}

func (m *MockReader) Read(p []byte) (n int, err error) {
	return m.ReadResult, m.ReadError
}

func (m *MockReader) Close() error {
	return m.CloseError
}
