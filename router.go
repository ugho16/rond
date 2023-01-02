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

package main

import (
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	swagger "github.com/davidebianchi/gswagger"
	"github.com/rond-authz/rond/internal/config"
	"github.com/rond-authz/rond/internal/metrics"
	"github.com/rond-authz/rond/internal/utils"
	"github.com/rond-authz/rond/openapi"
	"github.com/rond-authz/rond/types"

	"github.com/gorilla/mux"
)

var routesToNotProxy = utils.Union(statusRoutes, []string{metrics.MetricsRoutePath})

var revokeDefinitions = swagger.Definitions{
	RequestBody: &swagger.ContentValue{
		Content: swagger.Content{
			"application/json": {
				Value: RevokeRequestBody{},
			},
		},
	},
	Responses: map[int]swagger.ContentValue{
		http.StatusOK: {
			Content: swagger.Content{
				"application/json": {Value: RevokeResponseBody{}},
			},
		},
		http.StatusInternalServerError: {
			Content: swagger.Content{
				"application/json": {Value: types.RequestError{}},
			},
		},
		http.StatusBadRequest: {
			Content: swagger.Content{
				"application/json": {Value: types.RequestError{}},
			},
		},
	},
}

var grantDefinitions = swagger.Definitions{
	RequestBody: &swagger.ContentValue{
		Content: swagger.Content{
			"application/json": {
				Value: GrantRequestBody{},
			},
		},
	},
	Responses: map[int]swagger.ContentValue{
		http.StatusOK: {
			Content: swagger.Content{
				"application/json": {Value: GrantResponseBody{}},
			},
		},
		http.StatusInternalServerError: {
			Content: swagger.Content{
				"application/json": {Value: types.RequestError{}},
			},
		},
		http.StatusBadRequest: {
			Content: swagger.Content{
				"application/json": {Value: types.RequestError{}},
			},
		},
	},
}

func setupRoutes(router *mux.Router, oas *openapi.OpenAPISpec, env config.EnvironmentVariables) {
	var documentationPermission string
	documentationPathInOAS := oas.Paths[env.TargetServiceOASPath]
	if documentationPathInOAS != nil {
		if getVerb, ok := documentationPathInOAS[strings.ToLower(http.MethodGet)]; ok && getVerb.PermissionV2 != nil {
			documentationPermission = getVerb.PermissionV2.RequestFlow.PolicyName
		}
	}

	// NOTE: The following sort is required by mux router because it expects
	// routes to be registered in the proper order
	paths := make([]string, 0)
	methods := make(map[string][]string, 0)

	for path, pathMethods := range oas.Paths {
		paths = append(paths, path)
		for method := range pathMethods {
			if method == openapi.AllHTTPMethod {
				methods[path] = openapi.OasSupportedHTTPMethods
				continue
			}
			if methods[path] == nil {
				methods[path] = []string{}
			}

			methods[path] = append(methods[path], strings.ToUpper(method))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))

	for _, path := range paths {
		pathToRegister := path
		if env.Standalone {
			pathToRegister = fmt.Sprintf("%s%s", env.PathPrefixStandalone, path)
		}
		if utils.Contains(routesToNotProxy, pathToRegister) {
			continue
		}
		if strings.Contains(pathToRegister, "*") {
			pathWithoutAsterisk := strings.ReplaceAll(pathToRegister, "*", "")
			router.PathPrefix(openapi.ConvertPathVariablesToBrackets(pathWithoutAsterisk)).HandlerFunc(rbacHandler).Methods(methods[path]...)
			continue
		}
		if path == env.TargetServiceOASPath && documentationPermission == "" {
			router.HandleFunc(openapi.ConvertPathVariablesToBrackets(pathToRegister), alwaysProxyHandler).Methods(http.MethodGet)
			continue
		}
		router.HandleFunc(openapi.ConvertPathVariablesToBrackets(pathToRegister), rbacHandler).Methods(methods[path]...)
	}
	if documentationPathInOAS == nil {
		router.HandleFunc(openapi.ConvertPathVariablesToBrackets(env.TargetServiceOASPath), alwaysProxyHandler)
	}
	// FIXME: All the routes don't inserted above are anyway handled by rbacHandler.
	//        Maybe the code above can be cleaned.
	// NOTE: this fallback route should be removed in v2, check out
	// 			 issue [14](https://github.com/rond-authz/rond/issues/14) for further details.
	fallbackRoute := "/"
	if env.Standalone {
		fallbackRoute = fmt.Sprintf("%s/", path.Join(env.PathPrefixStandalone, fallbackRoute))
	}
	router.PathPrefix(fallbackRoute).HandlerFunc(rbacHandler)
}
