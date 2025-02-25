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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/rond-authz/rond/internal/config"
	"github.com/rond-authz/rond/internal/metrics"
	"github.com/rond-authz/rond/internal/mocks"
	"github.com/rond-authz/rond/internal/utils"
	"github.com/rond-authz/rond/openapi"
	"github.com/rond-authz/rond/types"

	"github.com/mia-platform/glogger/v2"
	"github.com/open-policy-agent/opa/topdown/print"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
)

func TestNewOPAEvaluator(t *testing.T) {
	envs := config.EnvironmentVariables{}
	input := map[string]interface{}{}
	inputBytes, _ := json.Marshal(input)
	t.Run("policy sanitization", func(t *testing.T) {
		evaluator, _ := NewOPAEvaluator(context.Background(), "very.composed.policy", &OPAModuleConfig{Content: "package policies very_composed_policy {true}"}, inputBytes, envs)

		result, err := evaluator.PolicyEvaluator.Eval(context.TODO())
		require.Nil(t, err, "unexpected error")
		require.True(t, result.Allowed(), "Unexpected failing policy")

		parialResult, err := evaluator.PolicyEvaluator.Partial(context.TODO())
		require.Nil(t, err, "unexpected error")
		require.Equal(t, 1, len(parialResult.Queries), "Unexpected failing policy")
	})
}

func TestCreateRegoInput(t *testing.T) {
	env := config.EnvironmentVariables{}
	user := types.User{}
	enableResourcePermissionsMapOptimization := false

	t.Run("headers", func(t *testing.T) {
		t.Run("allow empty userproperties header", func(t *testing.T) {
			env := config.EnvironmentVariables{
				UserPropertiesHeader: "userproperties",
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("userproperties", "")

			_, err := CreateRegoQueryInput(req, env, enableResourcePermissionsMapOptimization, user, nil)
			require.Nil(t, err, "Unexpected error")
		})

		t.Run("fail on invalid userproperties header value", func(t *testing.T) {
			env := config.EnvironmentVariables{
				UserPropertiesHeader: "userproperties",
			}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("userproperties", "1")

			_, err := CreateRegoQueryInput(req, env, enableResourcePermissionsMapOptimization, user, nil)
			require.Error(t, err)
		})
	})

	t.Run("body integration", func(t *testing.T) {
		expectedRequestBody := []byte(`{"Key":42}`)
		reqBody := struct{ Key int }{
			Key: 42,
		}
		reqBodyBytes, err := json.Marshal(reqBody)
		require.Nil(t, err, "Unexpected error")

		t.Run("ignored on method GET", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", bytes.NewReader(reqBodyBytes))

			inputBytes, err := CreateRegoQueryInput(req, env, enableResourcePermissionsMapOptimization, user, nil)
			require.Nil(t, err, "Unexpected error")
			require.True(t, !strings.Contains(string(inputBytes), fmt.Sprintf(`"body":%s`, expectedRequestBody)))
		})

		t.Run("ignore nil body on method POST", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Header.Set(utils.ContentTypeHeaderKey, "application/json")

			inputBytes, err := CreateRegoQueryInput(req, env, enableResourcePermissionsMapOptimization, user, nil)
			require.Nil(t, err, "Unexpected error")
			require.True(t, !strings.Contains(string(inputBytes), fmt.Sprintf(`"body":%s`, expectedRequestBody)))
		})

		t.Run("added on accepted methods", func(t *testing.T) {
			acceptedMethods := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

			for _, method := range acceptedMethods {
				req := httptest.NewRequest(method, "/", bytes.NewReader(reqBodyBytes))
				req.Header.Set(utils.ContentTypeHeaderKey, "application/json")
				inputBytes, err := CreateRegoQueryInput(req, env, enableResourcePermissionsMapOptimization, user, nil)
				require.Nil(t, err, "Unexpected error")

				require.True(t, strings.Contains(string(inputBytes), fmt.Sprintf(`"body":%s`, expectedRequestBody)), "Unexpected body for method %s", method)
			}
		})

		t.Run("added with content-type specifying charset", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(reqBodyBytes))
			req.Header.Set(utils.ContentTypeHeaderKey, "application/json;charset=UTF-8")
			inputBytes, err := CreateRegoQueryInput(req, env, enableResourcePermissionsMapOptimization, user, nil)
			require.Nil(t, err, "Unexpected error")

			require.True(t, strings.Contains(string(inputBytes), fmt.Sprintf(`"body":%s`, expectedRequestBody)), "Unexpected body for method %s", http.MethodPost)
		})

		t.Run("reject on method POST but with invalid body", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("{notajson}")))
			req.Header.Set(utils.ContentTypeHeaderKey, "application/json")
			_, err := CreateRegoQueryInput(req, env, enableResourcePermissionsMapOptimization, user, nil)
			require.True(t, err != nil)
		})

		t.Run("ignore body on method POST but with another content type", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("{notajson}")))
			req.Header.Set(utils.ContentTypeHeaderKey, "multipart/form-data")

			inputBytes, err := CreateRegoQueryInput(req, env, enableResourcePermissionsMapOptimization, user, nil)
			require.Nil(t, err, "Unexpected error")
			require.True(t, !strings.Contains(string(inputBytes), fmt.Sprintf(`"body":%s`, expectedRequestBody)))
		})
	})
}

func TestCreatePolicyEvaluators(t *testing.T) {
	t.Run("with simplified mock", func(t *testing.T) {
		log, _ := test.NewNullLogger()
		ctx := glogger.WithLogger(context.Background(), logrus.NewEntry(log))

		envs := config.EnvironmentVariables{
			APIPermissionsFilePath: "../mocks/simplifiedMock.json",
			OPAModulesDirectory:    "../mocks/rego-policies",
		}
		openApiSpec, err := openapi.LoadOASFromFileOrNetwork(log, envs)
		require.NoError(t, err, "unexpected error")

		opaModuleConfig, err := LoadRegoModule(envs.OPAModulesDirectory)
		require.NoError(t, err, "unexpected error")

		policyEvals, err := SetupEvaluators(ctx, nil, openApiSpec, opaModuleConfig, envs)
		require.NoError(t, err, "unexpected error creating evaluators")
		require.Len(t, policyEvals, 4, "unexpected length")
	})

	t.Run("with complete oas mock", func(t *testing.T) {
		log, _ := test.NewNullLogger()
		ctx := glogger.WithLogger(context.Background(), logrus.NewEntry(log))

		envs := config.EnvironmentVariables{
			APIPermissionsFilePath: "../mocks/pathsConfigAllInclusive.json",
			OPAModulesDirectory:    "../mocks/rego-policies",
		}
		openApiSpec, err := openapi.LoadOASFromFileOrNetwork(log, envs)
		require.NoError(t, err, "unexpected error")

		opaModuleConfig, err := LoadRegoModule(envs.OPAModulesDirectory)
		require.NoError(t, err, "unexpected error")

		policyEvals, err := SetupEvaluators(ctx, nil, openApiSpec, opaModuleConfig, envs)
		require.NoError(t, err, "unexpected error creating evaluators")
		require.Len(t, policyEvals, 4, "unexpected length")
	})
}

func TestBuildRolesMap(t *testing.T) {
	roles := []types.Role{
		{
			RoleID:      "role1",
			Permissions: []string{"permission1", "permission2"},
		},
		{
			RoleID:      "role2",
			Permissions: []string{"permission3", "permission4"},
		},
	}
	result := buildRolesMap(roles)
	expected := map[string][]string{
		"role1": {"permission1", "permission2"},
		"role2": {"permission3", "permission4"},
	}
	require.Equal(t, expected, result)
}

func TestBuildOptimizedResourcePermissionsMap(t *testing.T) {
	user := types.User{
		UserRoles: []types.Role{
			{
				RoleID:      "role1",
				Permissions: []string{"permission1", "permission2"},
			},
			{
				RoleID:      "role2",
				Permissions: []string{"permission3", "permission4"},
			},
		},
		UserBindings: []types.Binding{
			{
				Resource: &types.Resource{
					ResourceType: "type1",
					ResourceID:   "resource1",
				},
				Roles:       []string{"role1"},
				Permissions: []string{"permissionNotInRole1"},
			},
			{
				Resource: &types.Resource{
					ResourceType: "type2",
					ResourceID:   "resource2",
				},
				Roles: []string{"role2"},
			},
			{
				Resource: &types.Resource{
					ResourceType: "type3",
					ResourceID:   "resource3",
				},
				Roles:       []string{"role1", "role2"},
				Permissions: []string{"permissionNotInRole2", "permissionNotInRole3"},
			},
		},
	}
	result := buildOptimizedResourcePermissionsMap(user)
	expected := PermissionsOnResourceMap{
		"permission1:type1:resource1":          true,
		"permission2:type1:resource1":          true,
		"permissionNotInRole1:type1:resource1": true,
		"permission3:type2:resource2":          true,
		"permission4:type2:resource2":          true,
		"permission1:type3:resource3":          true,
		"permission2:type3:resource3":          true,
		"permission3:type3:resource3":          true,
		"permission4:type3:resource3":          true,
		"permissionNotInRole2:type3:resource3": true,
		"permissionNotInRole3:type3:resource3": true,
	}
	require.Equal(t, expected, result)
}
func TestCreateQueryEvaluator(t *testing.T) {
	envs := config.EnvironmentVariables{}
	policy := `package policies
allow {
	true
}
column_policy{
	false
}
`
	permission := openapi.XPermission{
		AllowPermission: "allow",
		ResponseFilter: openapi.ResponseFilterConfiguration{
			Policy: "column_policy",
		},
	}

	ctx := createContext(t,
		context.Background(),
		config.EnvironmentVariables{TargetServiceHost: "test"},
		nil,
		&openapi.RondConfig{
			RequestFlow:  openapi.RequestFlow{PolicyName: "allow"},
			ResponseFlow: openapi.ResponseFlow{PolicyName: "column_policy"},
		},

		&OPAModuleConfig{Name: "mypolicy.rego", Content: policy},
		nil,
	)

	r, err := http.NewRequestWithContext(ctx, "GET", "http://www.example.com:8080/api", nil)
	require.NoError(t, err, "Unexpected error")
	log, _ := test.NewNullLogger()
	logger := logrus.NewEntry(log)

	input := Input{Request: InputRequest{}, Response: InputResponse{}}
	inputBytes, _ := json.Marshal(input)

	t.Run("create  evaluator with allowPolicy", func(t *testing.T) {
		evaluator, err := CreateQueryEvaluator(context.Background(), logger, r, envs, permission.AllowPermission, inputBytes, nil)
		require.True(t, evaluator != nil)
		require.NoError(t, err, "Unexpected status code.")
	})

	t.Run("create  evaluator with policy for column filtering", func(t *testing.T) {
		evaluator, err := CreateQueryEvaluator(context.Background(), logger, r, envs, permission.ResponseFilter.Policy, inputBytes, nil)
		require.True(t, evaluator != nil)
		require.NoError(t, err, "Unexpected status code.")
	})
}

func BenchmarkBuildOptimizedResourcePermissionsMap(b *testing.B) {
	var roles []types.Role
	for i := 0; i < 20; i++ {
		role := types.Role{
			RoleID:      fmt.Sprintf("role%d", i),
			Permissions: []string{fmt.Sprintf("permission%d", i), fmt.Sprintf("permission%d", i+1)},
		}
		roles = append(roles, role)

	}
	var bindings []types.Binding
	for i := 0; i < 100; i++ {
		binding := types.Binding{
			Resource: &types.Resource{
				ResourceType: fmt.Sprintf("type%d", i),
				ResourceID:   fmt.Sprintf("resource%d", i),
			},
			Roles:       []string{fmt.Sprintf("role%d", i)},
			Permissions: []string{fmt.Sprintf("permissionRole%d", i)},
		}
		bindings = append(bindings, binding)

	}
	user := types.User{
		UserRoles:    roles,
		UserBindings: bindings,
	}

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		b.StartTimer()
		buildOptimizedResourcePermissionsMap(user)
		b.StopTimer()
	}
}

func TestPrint(t *testing.T) {
	var buf bytes.Buffer
	h := NewPrintHook(&buf, "policy-name")

	err := h.Print(print.Context{}, "the print message")
	require.NoError(t, err)

	var re = regexp.MustCompile(`"time":\d+`)
	require.JSONEq(t, `{"level":10,"msg":"the print message","time":123,"policyName":"policy-name"}`, string(re.ReplaceAll(buf.Bytes(), []byte("\"time\":123"))))
}

func createContext(
	t *testing.T,
	originalCtx context.Context,
	env config.EnvironmentVariables,
	mongoClient *mocks.MongoClientMock,
	permission *openapi.RondConfig,
	opaModuleConfig *OPAModuleConfig,
	partialResultEvaluators PartialResultsEvaluators,
) context.Context {
	t.Helper()

	var partialContext context.Context
	partialContext = context.WithValue(originalCtx, config.EnvKey{}, env)
	partialContext = context.WithValue(partialContext, openapi.XPermissionKey{}, permission)
	partialContext = context.WithValue(partialContext, OPAModuleConfigKey{}, opaModuleConfig)
	if mongoClient != nil {
		partialContext = context.WithValue(partialContext, types.MongoClientContextKey{}, mongoClient)
	}
	partialContext = context.WithValue(partialContext, PartialResultsEvaluatorConfigKey{}, partialResultEvaluators)

	log, _ := test.NewNullLogger()
	partialContext = glogger.WithLogger(partialContext, logrus.NewEntry(log))

	partialContext = context.WithValue(partialContext, openapi.RouterInfoKey{}, openapi.RouterInfo{
		MatchedPath:   "/matched/path",
		RequestedPath: "/requested/path",
		Method:        "GET",
	})

	partialContext = metrics.WithValue(partialContext, metrics.SetupMetrics("test_rond"))

	return partialContext
}
func TestGetHeaderFunction(t *testing.T) {
	headerKeyMocked := "exampleKey"
	headerValueMocked := "value"
	env := config.EnvironmentVariables{}

	opaModule := &OPAModuleConfig{
		Name: "example.rego",
		Content: `package policies
		todo { get_header("ExAmPlEkEy", input.headers) == "value" }`,
	}
	queryString := "todo"

	t.Run("if header key exists", func(t *testing.T) {
		headers := http.Header{}
		headers.Add(headerKeyMocked, headerValueMocked)
		input := map[string]interface{}{
			"headers": headers,
		}
		inputBytes, _ := json.Marshal(input)

		opaEvaluator, err := NewOPAEvaluator(context.Background(), queryString, opaModule, inputBytes, env)
		require.NoError(t, err, "Unexpected error during creation of opaEvaluator")

		results, err := opaEvaluator.PolicyEvaluator.Eval(context.TODO())
		require.NoError(t, err, "Unexpected error during rego validation")
		require.True(t, results.Allowed(), "The input is not allowed by rego")

		partialResults, err := opaEvaluator.PolicyEvaluator.Partial(context.TODO())
		require.NoError(t, err, "Unexpected error during rego validation")

		require.Len(t, partialResults.Queries, 1, "Rego policy allows illegal input")
	})

	t.Run("if header key not exists", func(t *testing.T) {
		input := map[string]interface{}{
			"headers": http.Header{},
		}
		inputBytes, _ := json.Marshal(input)

		opaEvaluator, err := NewOPAEvaluator(context.Background(), queryString, opaModule, inputBytes, env)
		require.NoError(t, err, "Unexpected error during creation of opaEvaluator")

		results, err := opaEvaluator.PolicyEvaluator.Eval(context.TODO())
		require.NoError(t, err, "Unexpected error during rego validation")
		require.True(t, !results.Allowed(), "Rego policy allows illegal input")

		partialResults, err := opaEvaluator.PolicyEvaluator.Partial(context.TODO())
		require.NoError(t, err, "Unexpected error during rego validation")

		require.Len(t, partialResults.Queries, 0, "Rego policy allows illegal input")
	})
}

func TestGetOPAModuleConfig(t *testing.T) {
	t.Run(`GetOPAModuleConfig fails because no key has been passed`, func(t *testing.T) {
		ctx := context.Background()
		env, err := GetOPAModuleConfig(ctx)
		require.True(t, err != nil, "An error was expected.")
		t.Logf("Expected error: %s - env: %+v", err.Error(), env)
	})

	t.Run(`GetOPAModuleConfig returns OPAEvaluator from context`, func(t *testing.T) {
		ctx := context.WithValue(context.Background(), OPAModuleConfigKey{}, &OPAModuleConfig{})
		opaEval, err := GetOPAModuleConfig(ctx)
		require.True(t, err == nil, "Unexpected error.")
		require.True(t, opaEval != nil, "OPA Module config not found.")
	})
}

func TestGetPolicyEvaluators(t *testing.T) {
	t.Run(`GetPolicyEvaluators fails because no key has been passed`, func(t *testing.T) {
		ctx := context.Background()
		env, err := GetPartialResultsEvaluators(ctx)
		require.True(t, err != nil, "An error was expected.")
		t.Logf("Expected error: %s - env: %+v", err.Error(), env)
	})

	t.Run(`GetPartialResultsEvaluators returns PartialResultsEvaluators from context`, func(t *testing.T) {
		ctx := context.WithValue(context.Background(), PartialResultsEvaluatorConfigKey{}, PartialResultsEvaluators{})
		opaEval, err := GetPartialResultsEvaluators(ctx)
		require.True(t, err == nil, "Unexpected error.")
		require.True(t, opaEval != nil, "OPA Module config not found.")
	})
}
