/*
 * Copyright © 2021-present Mia s.r.l.
 * All rights reserved
 */

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"rbac-service/internal/testutils"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"gopkg.in/h2non/gock.v1"
)

func TestEntryPoint(t *testing.T) {
	t.Run("fails for invalid module path, no module found", func(t *testing.T) {
		os.Setenv("HTTP_PORT", "3000")
		os.Setenv("TARGET_SERVICE_HOST", "localhost:3001")
		os.Setenv("TARGET_SERVICE_OAS_PATH", "/documentation/json")
		os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/empty-dir")

		shutdown := make(chan os.Signal, 1)

		entrypoint(shutdown)
		require.True(t, true, "If we get here the service has not started")
	})

	t.Run("opens server on port 3000", func(t *testing.T) {
		shutdown := make(chan os.Signal, 1)
		defer gock.Off()
		gock.EnableNetworking()
		gock.NetworkingFilter(func(r *http.Request) bool {
			return r.URL.Path != "/documentation/json"
		})
		gock.New("http://localhost:3001").
			Get("/documentation/json").
			Reply(200).
			File("./mocks/simplifiedMock.json")

		os.Setenv("HTTP_PORT", "3000")
		os.Setenv("TARGET_SERVICE_HOST", "localhost:3001")
		os.Setenv("TARGET_SERVICE_OAS_PATH", "/documentation/json")
		os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/rego-policies")

		go func() {
			entrypoint(shutdown)
		}()
		defer func() {
			os.Unsetenv("HTTP_PORT")
			shutdown <- syscall.SIGTERM
		}()

		time.Sleep(1 * time.Second)
		resp, err := http.DefaultClient.Get("http://localhost:3000/-/ready")
		require.Equal(t, nil, err)
		require.Equal(t, 200, resp.StatusCode)
	})

	t.Run("GracefulShutdown works properly", func(t *testing.T) {
		defer gock.Off()
		gock.New("http://localhost:3001").
			Get("/documentation/json").
			Reply(200).
			File("./mocks/simplifiedMock.json")

		os.Setenv("HTTP_PORT", "3000")
		os.Setenv("TARGET_SERVICE_HOST", "localhost:3001")
		os.Setenv("TARGET_SERVICE_OAS_PATH", "/documentation/json")
		os.Setenv("DELAY_SHUTDOWN_SECONDS", "3")
		os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/rego-policies")

		shutdown := make(chan os.Signal, 1)
		done := make(chan bool, 1)

		go func() {
			time.Sleep(5 * time.Second)
			done <- false
		}()

		go func() {
			entrypoint(shutdown)
			done <- true
		}()
		shutdown <- syscall.SIGTERM

		flag := <-done
		require.Equal(t, true, flag)
	})

	t.Run("opa integration", func(t *testing.T) {
		shutdown := make(chan os.Signal, 1)

		defer gock.Off()
		gock.EnableNetworking()
		gock.NetworkingFilter(func(r *http.Request) bool {
			if r.URL.Path == "/documentation/json" {
				return false
			}
			if r.URL.Path == "/users/" && r.URL.Host == "localhost:3001" {
				return false
			}
			return true
		})

		gock.New("http://localhost:3001").
			Get("/documentation/json").
			Reply(200).
			File("./mocks/simplifiedMock.json")

		os.Setenv("HTTP_PORT", "3000")
		os.Setenv("TARGET_SERVICE_HOST", "localhost:3001")
		os.Setenv("TARGET_SERVICE_OAS_PATH", "/documentation/json")
		os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/rego-policies")

		go func() {
			entrypoint(shutdown)
		}()
		defer func() {
			os.Unsetenv("HTTP_PORT")
			shutdown <- syscall.SIGTERM
		}()
		time.Sleep(1 * time.Second)

		t.Run("ok - opa evaluation success", func(t *testing.T) {
			gock.Flush()
			gock.New("http://localhost:3001/users/").
				Get("/users/").
				Reply(200)
			resp, err := http.DefaultClient.Get("http://localhost:3000/users/")

			require.Equal(t, nil, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.True(t, gock.IsDone(), "the proxy blocks the request when the permissions are granted.")
		})

		t.Run("forbidden - opa evaluation fail", func(t *testing.T) {
			gock.Flush()
			gock.New("http://localhost:3001/").
				Post("/users/").
				Reply(200)
			resp, err := http.DefaultClient.Post("http://localhost:3000/users/", "text/plain", nil)
			require.Equal(t, nil, err)
			require.Equal(t, http.StatusForbidden, resp.StatusCode, "unexpected status code.")
			require.False(t, gock.IsDone(), "the proxy forwards the request when the permissions aren't granted.")
		})
	})

	t.Run("x-permissions is empty", func(t *testing.T) {
		gock.Flush()
		shutdown := make(chan os.Signal, 1)

		defer gock.Off()
		gock.EnableNetworking()
		gock.NetworkingFilter(func(r *http.Request) bool {
			if r.URL.Path == "/documentation/json" {
				return false
			}
			if r.URL.Path == "/users/" && r.URL.Host == "localhost:3004" {
				return false
			}
			return true
		})

		gock.New("http://localhost:3004").
			Get("/documentation/json").
			Reply(200).
			File("./mocks/mockWithXPermissionEmpty.json")

		os.Setenv("HTTP_PORT", "3005")
		os.Setenv("TARGET_SERVICE_HOST", "localhost:3004")
		os.Setenv("TARGET_SERVICE_OAS_PATH", "/documentation/json")
		os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/rego-policies")

		go func() {
			entrypoint(shutdown)
		}()
		defer func() {
			os.Unsetenv("HTTP_PORT")
			shutdown <- syscall.SIGTERM
		}()
		time.Sleep(1 * time.Second)

		gock.New("http://localhost:3004/").
			Post("/users/").
			Reply(200)
		resp, err := http.DefaultClient.Post("http://localhost:3005/users/", "text/plain", nil)
		require.Equal(t, nil, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "unexpected status code.")
		require.False(t, gock.IsDone(), "the proxy forwards the request when the permissions aren't granted.")
	})

	t.Run("api permissions file path with nested routes (/*)", func(t *testing.T) {
		gock.Flush()
		shutdown := make(chan os.Signal, 1)

		defer gock.Off()
		defer gock.DisableNetworkingFilters()

		gock.EnableNetworking()
		gock.NetworkingFilter(func(r *http.Request) bool {
			if r.URL.Path == "/documentation/json" {
				return false
			}
			if r.URL.Path == "/foo/bar/not/registered/explicitly" && r.URL.Host == "localhost:4000" {
				return false
			}
			return true
		})

		os.Setenv("HTTP_PORT", "3333")
		os.Setenv("TARGET_SERVICE_HOST", "localhost:4000")
		os.Setenv("API_PERMISSIONS_FILE_PATH", "./mocks/nestedPathsConfig.json")
		os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/rego-policies")

		go func() {
			entrypoint(shutdown)
		}()
		defer func() {
			os.Unsetenv("HTTP_PORT")
			os.Unsetenv("API_PERMISSIONS_FILE_PATH")
			shutdown <- syscall.SIGTERM
		}()
		time.Sleep(1 * time.Second)

		gock.New("http://localhost:4000/").
			Get("foo/bar/not/registered/explicitly").
			Reply(200)

		resp, err := http.DefaultClient.Get("http://localhost:3333/foo/bar/not/registered/explicitly")
		require.Equal(t, nil, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status code.")
		require.True(t, gock.IsDone(), "the proxy forwards the request when the permissions aren't granted.")
	})

	t.Run("mongo integration", func(t *testing.T) {
		shutdown := make(chan os.Signal, 1)

		defer gock.Off()
		gock.EnableNetworking()
		gock.NetworkingFilter(func(r *http.Request) bool {
			if r.URL.Path == "/documentation/json" {
				return false
			}
			if r.URL.Path == "/users/" && r.URL.Host == "localhost:3002" {
				return false
			}
			return true
		})

		gock.New("http://localhost:3002").
			Get("/documentation/json").
			Reply(200).
			File("./mocks/simplifiedMock.json")
		os.Setenv("HTTP_PORT", "3003")
		os.Setenv("TARGET_SERVICE_HOST", "localhost:3002")
		os.Setenv("TARGET_SERVICE_OAS_PATH", "/documentation/json")
		os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/rego-policies")
		mongoHost := os.Getenv("MONGO_HOST_CI")
		if mongoHost == "" {
			mongoHost = testutils.LocalhostMongoDB
			t.Logf("Connection to localhost MongoDB, on CI env this is a problem!")
		}
		randomizedDBNamePart := testutils.GetRandomName(10)
		mongoDBName := fmt.Sprintf("test-%s", randomizedDBNamePart)
		os.Setenv("MONGODB_URL", fmt.Sprintf("mongodb://%s/%s", mongoHost, mongoDBName))
		os.Setenv("BINDINGS_COLLECTION_NAME", "bindings")
		os.Setenv("ROLES_COLLECTION_NAME", "roles")

		clientOpts := options.Client().ApplyURI(fmt.Sprintf("mongodb://%s", mongoHost))
		client, err := mongo.Connect(context.Background(), clientOpts)
		if err != nil {
			fmt.Printf("error connecting to MongoDB: %s", err.Error())
		}

		ctx, cancelFn := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelFn()
		if err = client.Ping(ctx, readpref.Primary()); err != nil {
			fmt.Printf("error verifying MongoDB connection: %s", err.Error())
		}
		mongoClient := MongoClient{
			client:   client,
			roles:    client.Database(mongoDBName).Collection("roles"),
			bindings: client.Database(mongoDBName).Collection("bindings"),
		}
		defer mongoClient.client.Disconnect(ctx)

		PopulateDbForTesting(t, ctx, &mongoClient)

		go func() {
			entrypoint(shutdown)
		}()
		defer func() {
			os.Unsetenv("HTTP_PORT")
			shutdown <- syscall.SIGTERM
		}()
		time.Sleep(1 * time.Second)

		t.Run("403 - without headers and collections", func(t *testing.T) {
			gock.Flush()
			gock.New("http://localhost:3002/users/").
				Get("/users/").
				Reply(200)
			resp, err := http.DefaultClient.Get("http://localhost:3003/users/")
			require.Equal(t, nil, err)
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
		})
		t.Run("200 - integration passed", func(t *testing.T) {
			gock.Flush()
			gock.New("http://localhost:3002/users/").
				Get("/users/").
				Reply(200)
			req, err := http.NewRequest("GET", "http://localhost:3003/users/", nil)
			req.Header.Set("miauserid", "user1")
			req.Header.Set("miausergroups", "user1,user2")
			client := &http.Client{}
			resp, err := client.Do(req)
			require.Equal(t, nil, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
		})
		t.Run("200 - integration passed without groups", func(t *testing.T) {
			gock.Flush()
			gock.New("http://localhost:3002/users/").
				Get("/users/").
				Reply(200)
			req, err := http.NewRequest("GET", "http://localhost:3003/users/", nil)
			req.Header.Set("miauserid", "user1")
			client := &http.Client{}
			resp, err := client.Do(req)
			require.Equal(t, nil, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
		})
	})

	t.Run("Skip opa evaluation when request url is equal to TargetServiceOASPath", func(t *testing.T) {
		shutdown := make(chan os.Signal, 1)

		t.Run("without oas documentation api defined", func(t *testing.T) {
			defer gock.Off()
			gock.EnableNetworking()
			gock.NetworkingFilter(func(r *http.Request) bool {
				return r.URL.Path != "/custom/documentation/json" && r.URL.Host == "localhost:3000"
			})
			gock.New("http://localhost:3001").
				Get("/custom/documentation/json").
				Reply(200).
				File("./mocks/simplifiedMock.json")

			os.Setenv("HTTP_PORT", "3000")
			os.Setenv("TARGET_SERVICE_HOST", "localhost:3001")
			os.Setenv("TARGET_SERVICE_OAS_PATH", "/custom/documentation/json")
			os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/rego-policies")
			go func() {
				entrypoint(shutdown)
			}()
			defer func() {
				os.Unsetenv("HTTP_PORT")
				shutdown <- syscall.SIGTERM
			}()
			time.Sleep(1 * time.Second)

			gock.Flush()
			gock.New("http://localhost:3000").
				Get("/custom/documentation/json").
				Reply(200)
			resp, err := http.DefaultClient.Get("http://localhost:3000/custom/documentation/json")

			require.Equal(t, nil, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.True(t, gock.IsDone(), "the proxy blocks the request when the permissions are granted.")
		})

		t.Run("with oas documentation api defined", func(t *testing.T) {
			defer gock.Off()
			gock.EnableNetworking()
			gock.NetworkingFilter(func(r *http.Request) bool {
				return r.URL.Path != "/documentation/json" && r.URL.Host == "localhost:3000"
			})
			gock.New("http://localhost:3001").
				Get("/documentation/json").
				Reply(200).
				File("./mocks/documentationPathMock.json")

			os.Setenv("HTTP_PORT", "3000")
			os.Setenv("TARGET_SERVICE_HOST", "localhost:3001")
			os.Setenv("TARGET_SERVICE_OAS_PATH", "/documentation/json")
			os.Setenv("OPA_MODULES_DIRECTORY", "./mocks/rego-policies")
			go func() {
				entrypoint(shutdown)
			}()
			defer func() {
				os.Unsetenv("HTTP_PORT")
				shutdown <- syscall.SIGTERM
			}()
			time.Sleep(1 * time.Second)

			gock.Flush()
			gock.New("http://localhost:3000").
				Get("/documentation/json").
				Reply(200)
			resp, err := http.DefaultClient.Get("http://localhost:3000/documentation/json")

			require.Equal(t, nil, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.True(t, gock.IsDone(), "the proxy blocks the request when the permissions are granted.")
		})
	})
}
