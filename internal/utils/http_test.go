package utils

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rond-authz/rond/internal/types"
	"github.com/stretchr/testify/require"
)

func TestUnmarshalHeader(t *testing.T) {
	userPropertiesHeaderKey := "miauserproperties"
	mockedUserProperties := map[string]interface{}{
		"my":  "other",
		"key": []string{"is", "not"},
	}
	mockedUserPropertiesStringified, err := json.Marshal(mockedUserProperties)
	require.NoError(t, err)

	t.Run("header not exists", func(t *testing.T) {
		headers := http.Header{}
		var userProperties map[string]interface{}

		ok, err := UnmarshalHeader(headers, userPropertiesHeaderKey, &userProperties)

		require.True(t, !ok, "Unmarshal not existing header")
		require.NoError(t, err, "Unexpected error if doesn't exist header")
	})

	t.Run("header exists but the unmarshalling fails", func(t *testing.T) {
		headers := http.Header{}
		headers.Set(userPropertiesHeaderKey, string(mockedUserPropertiesStringified))
		var userProperties string

		ok, err := UnmarshalHeader(headers, userPropertiesHeaderKey, &userProperties)
		require.False(t, ok, "Unexpected success during unmarshalling")
		var unmarshalErr = &json.UnmarshalTypeError{}
		require.ErrorAs(t, err, &unmarshalErr, "Unexpected error on unmarshalling")
	})

	t.Run("header exists and unmarshalling finishes correctly", func(t *testing.T) {
		headers := http.Header{}
		headers.Set(userPropertiesHeaderKey, string(mockedUserPropertiesStringified))
		var userProperties map[string]interface{}

		ok, err := UnmarshalHeader(headers, userPropertiesHeaderKey, &userProperties)
		require.True(t, ok, "Unexpected failure")
		require.NoError(t, err, "Unexpected error")
	})
}

func TestFailResponseWithCode(t *testing.T) {
	w := httptest.NewRecorder()

	FailResponseWithCode(w, http.StatusInternalServerError, "The Error", "The Message")
	require.Equal(t, http.StatusInternalServerError, w.Result().StatusCode)

	require.Equal(t, JSONContentTypeHeader, w.Result().Header.Get(ContentTypeHeaderKey))

	bodyBytes, err := io.ReadAll(w.Body)
	require.NoError(t, err)

	var response types.RequestError
	err = json.Unmarshal(bodyBytes, &response)
	require.NoError(t, err)

	require.Equal(t, types.RequestError{
		StatusCode: http.StatusInternalServerError,
		Error:      "The Error",
		Message:    "The Message",
	}, response)
}
