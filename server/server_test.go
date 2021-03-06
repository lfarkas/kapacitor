package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgrijalva/jwt-go"
	iclient "github.com/influxdata/influxdb/client/v2"
	"github.com/influxdata/influxdb/influxql"
	"github.com/influxdata/influxdb/models"
	"github.com/influxdata/influxdb/toml"
	"github.com/influxdata/kapacitor/alert"
	"github.com/influxdata/kapacitor/client/v1"
	"github.com/influxdata/kapacitor/command"
	"github.com/influxdata/kapacitor/command/commandtest"
	"github.com/influxdata/kapacitor/server"
	alertservice "github.com/influxdata/kapacitor/services/alert"
	"github.com/influxdata/kapacitor/services/alert/alerttest"
	"github.com/influxdata/kapacitor/services/alerta/alertatest"
	"github.com/influxdata/kapacitor/services/hipchat/hipchattest"
	"github.com/influxdata/kapacitor/services/opsgenie"
	"github.com/influxdata/kapacitor/services/opsgenie/opsgenietest"
	"github.com/influxdata/kapacitor/services/pagerduty"
	"github.com/influxdata/kapacitor/services/pagerduty/pagerdutytest"
	"github.com/influxdata/kapacitor/services/sensu/sensutest"
	"github.com/influxdata/kapacitor/services/slack/slacktest"
	"github.com/influxdata/kapacitor/services/smtp/smtptest"
	"github.com/influxdata/kapacitor/services/snmptrap/snmptraptest"
	"github.com/influxdata/kapacitor/services/talk/talktest"
	"github.com/influxdata/kapacitor/services/telegram"
	"github.com/influxdata/kapacitor/services/telegram/telegramtest"
	"github.com/influxdata/kapacitor/services/udf"
	"github.com/influxdata/kapacitor/services/victorops"
	"github.com/influxdata/kapacitor/services/victorops/victoropstest"
	"github.com/k-sone/snmpgo"
	"github.com/pkg/errors"
)

var udfDir string

func init() {
	dir, _ := os.Getwd()
	udfDir = filepath.Clean(filepath.Join(dir, "../udf"))
}

func TestServer_Ping(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()
	_, version, err := cli.Ping()
	if err != nil {
		t.Fatal(err)
	}
	if version != "testServer" {
		t.Fatal("unexpected version", version)
	}
}

func TestServer_Pprof_Index(t *testing.T) {
	s, _ := OpenDefaultServer()
	defer s.Close()
	testCases := []struct {
		path        string
		code        int
		contentType string
	}{
		{
			path:        "/debug/pprof/",
			code:        http.StatusOK,
			contentType: "text/html; charset=utf-8",
		},
		{
			path:        "/debug/pprof/block",
			code:        http.StatusOK,
			contentType: "text/plain; charset=utf-8",
		},
		{
			path:        "/debug/pprof/goroutine",
			code:        http.StatusOK,
			contentType: "text/plain; charset=utf-8",
		},
		{
			path:        "/debug/pprof/heap",
			code:        http.StatusOK,
			contentType: "text/plain; charset=utf-8",
		},
		{
			path:        "/debug/pprof/threadcreate",
			code:        http.StatusOK,
			contentType: "text/plain; charset=utf-8",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			r, err := http.Get(s.URL() + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got, exp := r.StatusCode, tc.code; got != exp {
				t.Errorf("unexpected status code got %d exp %d", got, exp)
			}
			if got, exp := r.Header.Get("Content-Type"), tc.contentType; got != exp {
				t.Errorf("unexpected content type got %s exp %s", got, exp)
			}
		})
	}
}
func TestServer_Authenticate_Fail(t *testing.T) {
	conf := NewConfig()
	conf.HTTP.AuthEnabled = true
	s := OpenServer(conf)
	cli, err := client.New(client.Config{
		URL: s.URL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _, err = cli.Ping()
	if err == nil {
		t.Error("expected authentication error")
	} else if exp, got := "unable to parse authentication credentials", err.Error(); got != exp {
		t.Errorf("unexpected error message: got %q exp %q", got, exp)
	}
}

func TestServer_Authenticate_User(t *testing.T) {
	conf := NewConfig()
	conf.HTTP.AuthEnabled = true
	s := OpenServer(conf)
	cli, err := client.New(client.Config{
		URL: s.URL(),
		Credentials: &client.Credentials{
			Method:   client.UserAuthentication,
			Username: "bob",
			Password: "bob's secure password",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, version, err := cli.Ping()
	if err != nil {
		t.Fatal(err)
	}
	if version != "testServer" {
		t.Fatal("unexpected version", version)
	}
}

func TestServer_Authenticate_Bearer_Fail(t *testing.T) {
	secret := "secret"
	// Create a new token object, specifying signing method and the claims
	// you would like it to contain.
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, jwt.MapClaims{
		"username": "bob",
		"exp":      time.Now().Add(10 * time.Second).Unix(),
	})

	// Sign and get the complete encoded token as a string using the secret
	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}

	conf := NewConfig()
	conf.HTTP.AuthEnabled = true
	// Use a different secret so the token is invalid
	conf.HTTP.SharedSecret = secret + "extra secret"
	s := OpenServer(conf)
	cli, err := client.New(client.Config{
		URL: s.URL(),
		Credentials: &client.Credentials{
			Method: client.BearerAuthentication,
			Token:  tokenString,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _, err = cli.Ping()
	if err == nil {
		t.Error("expected authentication error")
	} else if exp, got := "invalid token: signature is invalid", err.Error(); got != exp {
		t.Errorf("unexpected error message: got %q exp %q", got, exp)
	}
}

func TestServer_Authenticate_Bearer_Expired(t *testing.T) {
	secret := "secret"
	// Create a new token object, specifying signing method and the claims
	// you would like it to contain.
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, jwt.MapClaims{
		"username": "bob",
		"exp":      time.Now().Add(-10 * time.Second).Unix(),
	})

	// Sign and get the complete encoded token as a string using the secret
	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}

	conf := NewConfig()
	conf.HTTP.AuthEnabled = true
	conf.HTTP.SharedSecret = secret
	s := OpenServer(conf)
	cli, err := client.New(client.Config{
		URL: s.URL(),
		Credentials: &client.Credentials{
			Method: client.BearerAuthentication,
			Token:  tokenString,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, _, err = cli.Ping()
	if err == nil {
		t.Error("expected authentication error")
	} else if exp, got := "invalid token: Token is expired", err.Error(); got != exp {
		t.Errorf("unexpected error message: got %q exp %q", got, exp)
	}
}

func TestServer_Authenticate_Bearer(t *testing.T) {
	secret := "secret"
	// Create a new token object, specifying signing method and the claims
	// you would like it to contain.
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, jwt.MapClaims{
		"username": "bob",
		"exp":      time.Now().Add(10 * time.Second).Unix(),
	})

	// Sign and get the complete encoded token as a string using the secret
	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}

	conf := NewConfig()
	conf.HTTP.AuthEnabled = true
	conf.HTTP.SharedSecret = secret
	s := OpenServer(conf)
	cli, err := client.New(client.Config{
		URL: s.URL(),
		Credentials: &client.Credentials{
			Method: client.BearerAuthentication,
			Token:  tokenString,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, version, err := cli.Ping()
	if err != nil {
		t.Fatal(err)
	}
	if version != "testServer" {
		t.Fatal("unexpected version", version)
	}
}

func TestServer_CreateTask(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Disabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Disabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	dot := "digraph testTaskID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
}

func TestServer_EnableTask(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Enabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Enabled)
	}
	if ti.Executing != true {
		t.Fatalf("unexpected executing got %v exp %v", ti.Executing, true)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	dot := `digraph testTaskID {
graph [throughput="0.00 points/s"];

stream0 [avg_exec_time_ns="0s" ];
stream0 -> from1 [processed="0"];

from1 [avg_exec_time_ns="0s" ];
}`
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
}

func TestServer_EnableTaskOnCreate(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Enabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Enabled)
	}
	if ti.Executing != true {
		t.Fatalf("unexpected executing got %v exp %v", ti.Executing, true)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	dot := `digraph testTaskID {
graph [throughput="0.00 points/s"];

stream0 [avg_exec_time_ns="0s" ];
stream0 -> from1 [processed="0"];

from1 [avg_exec_time_ns="0s" ];
}`
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
}

func TestServer_DisableTask(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Disabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Disabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	dot := "digraph testTaskID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
}

func TestServer_DeleteTask(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = cli.DeleteTask(task.Link)
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Task(task.Link, nil)
	if err == nil {
		t.Fatal("unexpected task:", ti)
	}
}

func TestServer_TaskNums(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`

	// Create a bunch of tasks with every 3rd task enabled
	count := 100
	enabled := 0
	tasks := make([]client.Task, count)
	for i := 0; i < count; i++ {
		status := client.Disabled
		if i%3 == 0 {
			enabled++
			status = client.Enabled
		}
		task, err := cli.CreateTask(client.CreateTaskOptions{
			ID:         fmt.Sprintf("%s-%d", id, i),
			Type:       ttype,
			DBRPs:      dbrps,
			TICKscript: tick,
			Status:     status,
		})
		if err != nil {
			t.Fatal(err)
		}
		tasks[i] = task
	}
	if stats, err := s.Stats(); err != nil {
		t.Fatal(err)
	} else {
		if got, exp := stats.NumTasks, count; got != exp {
			t.Errorf("unexpected num_tasks got %d exp %d", got, exp)
		}
		if got, exp := stats.NumEnabledTasks, enabled; got != exp {
			t.Errorf("unexpected num_enabled_tasks got %d exp %d", got, exp)
		}
	}

	// Enable a bunch of tasks
	for i, task := range tasks {
		if i%2 == 0 && task.Status != client.Enabled {
			enabled++
			tasks[i].Status = client.Enabled
			if _, err := cli.UpdateTask(task.Link, client.UpdateTaskOptions{
				Status: client.Enabled,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	if stats, err := s.Stats(); err != nil {
		t.Fatal(err)
	} else {
		if got, exp := stats.NumTasks, count; got != exp {
			t.Errorf("unexpected num_tasks got %d exp %d", got, exp)
		}
		if got, exp := stats.NumEnabledTasks, enabled; got != exp {
			t.Errorf("unexpected num_enabled_tasks got %d exp %d", got, exp)
		}
	}

	// Disable a bunch of tasks
	for i, task := range tasks {
		if i%5 == 0 && task.Status != client.Disabled {
			enabled--
			tasks[i].Status = client.Disabled
			if _, err := cli.UpdateTask(task.Link, client.UpdateTaskOptions{
				Status: client.Disabled,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	if stats, err := s.Stats(); err != nil {
		t.Fatal(err)
	} else {
		if got, exp := stats.NumTasks, count; got != exp {
			t.Errorf("unexpected num_tasks got %d exp %d", got, exp)
		}
		if got, exp := stats.NumEnabledTasks, enabled; got != exp {
			t.Errorf("unexpected num_enabled_tasks got %d exp %d", got, exp)
		}
	}

	// Delete a bunch of tasks
	for i, task := range tasks {
		if i%6 == 0 {
			count--
			if task.Status == client.Enabled {
				enabled--
			}
			if err := cli.DeleteTask(task.Link); err != nil {
				t.Fatal(err)
			}
		}
	}

	if stats, err := s.Stats(); err != nil {
		t.Fatal(err)
	} else {
		if got, exp := stats.NumTasks, count; got != exp {
			t.Errorf("unexpected num_tasks got %d exp %d", got, exp)
		}
		if got, exp := stats.NumEnabledTasks, enabled; got != exp {
			t.Errorf("unexpected num_enabled_tasks got %d exp %d", got, exp)
		}
	}

}
func TestServer_ListTasks(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()
	count := 10

	ttype := client.StreamTask
	tick := `stream
    |from()
        .measurement('test')
`

	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("testTaskID%d", i)
		status := client.Disabled
		if i%2 == 0 {
			status = client.Enabled
		}
		_, err := cli.CreateTask(client.CreateTaskOptions{
			ID:         id,
			Type:       ttype,
			DBRPs:      dbrps,
			TICKscript: tick,
			Status:     status,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := cli.ListTasks(nil)
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := count, len(tasks); exp != got {
		t.Fatalf("unexpected number of tasks: exp:%d got:%d", exp, got)
	}
	for i, task := range tasks {
		if exp, got := fmt.Sprintf("testTaskID%d", i), task.ID; exp != got {
			t.Errorf("unexpected task.ID i:%d exp:%s got:%s", i, exp, got)
		}
		if exp, got := client.StreamTask, task.Type; exp != got {
			t.Errorf("unexpected task.Type i:%d exp:%v got:%v", i, exp, got)
		}
		if !reflect.DeepEqual(task.DBRPs, dbrps) {
			t.Fatalf("unexpected dbrps i:%d exp:%s got:%s", i, dbrps, task.DBRPs)
		}
		exp := client.Disabled
		if i%2 == 0 {
			exp = client.Enabled
		}
		if got := task.Status; exp != got {
			t.Errorf("unexpected task.Status i:%d exp:%v got:%v", i, exp, got)
		}
		if exp, got := i%2 == 0, task.Executing; exp != got {
			t.Errorf("unexpected task.Executing i:%d exp:%v got:%v", i, exp, got)
		}
		if exp, got := true, len(task.Dot) != 0; exp != got {
			t.Errorf("unexpected task.Dot i:%d exp:\n%v\ngot:\n%v\n", i, exp, got)
		}
		if exp, got := tick, task.TICKscript; exp != got {
			t.Errorf("unexpected task.TICKscript i:%d exp:%v got:%v", i, exp, got)
		}
		if exp, got := "", task.Error; exp != got {
			t.Errorf("unexpected task.Error i:%d exp:%v got:%v", i, exp, got)
		}
	}

}

func TestServer_ListTasks_Fields(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()
	count := 100

	ttype := client.StreamTask
	tick := `stream
    |from()
        .measurement('test')
`
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("testTaskID%d", i)
		_, err := cli.CreateTask(client.CreateTaskOptions{
			ID:         id,
			Type:       ttype,
			DBRPs:      dbrps,
			TICKscript: tick,
			Status:     client.Enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := cli.ListTasks(&client.ListTasksOptions{
		Pattern: "testTaskID1*",
		Fields:  []string{"type", "status"},
		Offset:  1,
		Limit:   5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := 5, len(tasks); exp != got {
		t.Fatalf("unexpected number of tasks: exp:%d got:%d", exp, got)
	}
	for i, task := range tasks {
		if exp, got := fmt.Sprintf("testTaskID1%d", i), task.ID; exp != got {
			t.Errorf("unexpected task.ID i:%d exp:%s got:%s", i, exp, got)
		}
		if exp, got := client.StreamTask, task.Type; exp != got {
			t.Errorf("unexpected task.Type i:%d exp:%v got:%v", i, exp, got)
		}
		if exp, got := client.Enabled, task.Status; exp != got {
			t.Errorf("unexpected task.Status i:%d exp:%v got:%v", i, exp, got)
		}
		// We didn't request these fields so they should be default zero values
		if exp, got := 0, len(task.DBRPs); exp != got {
			t.Fatalf("unexpected dbrps i:%d exp:%d got:%d", i, exp, got)
		}
		if exp, got := false, task.Executing; exp != got {
			t.Errorf("unexpected task.Executing i:%d exp:%v got:%v", i, exp, got)
		}
		if exp, got := "", task.Dot; exp != got {
			t.Errorf("unexpected task.Dot i:%d exp:%v got:%v", i, exp, got)
		}
		if exp, got := "", task.TICKscript; exp != got {
			t.Errorf("unexpected task.TICKscript i:%d exp:%v got:%v", i, exp, got)
		}
		if exp, got := "", task.Error; exp != got {
			t.Errorf("unexpected task.Error i:%d exp:%v got:%v", i, exp, got)
		}
	}
}

func TestServer_CreateTemplate(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTemplateID"
	ttype := client.StreamTask
	tick := `var x = 5

stream
    |from()
        .measurement('test')
`
	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         id,
		Type:       ttype,
		TICKscript: tick,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Template(template.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got\n%s\nexp\n%s\n", ti.TICKscript, tick)
	}
	dot := "digraph testTemplateID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
	vars := client.Vars{"x": {Value: int64(5), Type: client.VarInt}}
	if !reflect.DeepEqual(vars, ti.Vars) {
		t.Fatalf("unexpected vars\ngot\n%s\nexp\n%s\n", ti.Vars, vars)
	}
}
func TestServer_UpdateTemplateID(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTemplateID"
	ttype := client.StreamTask
	tick := `var x = 5

stream
    |from()
        .measurement('test')
`
	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         id,
		Type:       ttype,
		TICKscript: tick,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Template(template.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got\n%s\nexp\n%s\n", ti.TICKscript, tick)
	}
	dot := "digraph testTemplateID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
	vars := client.Vars{"x": {Value: int64(5), Type: client.VarInt}}
	if !reflect.DeepEqual(vars, ti.Vars) {
		t.Fatalf("unexpected vars\ngot\n%s\nexp\n%s\n", ti.Vars, vars)
	}

	newID := "newTemplateID"
	template, err = cli.UpdateTemplate(template.Link, client.UpdateTemplateOptions{
		ID: newID,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, exp := template.Link.Href, "/kapacitor/v1/templates/newTemplateID"; got != exp {
		t.Fatalf("unexpected template link got %s exp %s", got, exp)
	}

	ti, err = cli.Template(template.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != newID {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, newID)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got\n%s\nexp\n%s\n", ti.TICKscript, tick)
	}
	dot = "digraph newTemplateID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
	if !reflect.DeepEqual(vars, ti.Vars) {
		t.Fatalf("unexpected vars\ngot\n%s\nexp\n%s\n", ti.Vars, vars)
	}
}
func TestServer_UpdateTemplateID_WithTasks(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTemplateID"
	ttype := client.StreamTask
	tick := `var x = 5

stream
    |from()
        .measurement('test')
`
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}

	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         id,
		Type:       ttype,
		TICKscript: tick,
	})
	if err != nil {
		t.Fatal(err)
	}

	count := 100
	tasks := make([]client.Task, count)
	for i := 0; i < count; i++ {
		task, err := cli.CreateTask(client.CreateTaskOptions{
			TemplateID: template.ID,
			DBRPs:      dbrps,
			Status:     client.Enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
		tasks[i] = task
	}

	newID := "newTemplateID"
	template, err = cli.UpdateTemplate(template.Link, client.UpdateTemplateOptions{
		ID: newID,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, task := range tasks {
		got, err := cli.Task(task.Link, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.TemplateID != newID {
			t.Errorf("unexpected task TemplateID got %s exp %s", got.TemplateID, newID)
		}
		if got.TICKscript != tick {
			t.Errorf("unexpected task TICKscript got %s exp %s", got.TICKscript, tick)
		}
	}
}
func TestServer_UpdateTemplateID_Fail(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTemplateID"
	newID := "anotherTemplateID"
	ttype := client.StreamTask
	tick := `var x = 5

stream
    |from()
        .measurement('test')
`
	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         id,
		Type:       ttype,
		TICKscript: tick,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Template(template.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got\n%s\nexp\n%s\n", ti.TICKscript, tick)
	}
	dot := "digraph testTemplateID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
	vars := client.Vars{"x": {Value: int64(5), Type: client.VarInt}}
	if !reflect.DeepEqual(vars, ti.Vars) {
		t.Fatalf("unexpected vars\ngot\n%s\nexp\n%s\n", ti.Vars, vars)
	}

	// Create conflicting template
	if _, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         newID,
		Type:       ttype,
		TICKscript: tick,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = cli.UpdateTemplate(template.Link, client.UpdateTemplateOptions{
		ID: newID,
	}); err == nil {
		t.Fatal("expected update template to fail on name conflict")
	}

	// Can still get old template
	ti, err = cli.Template(template.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got\n%s\nexp\n%s\n", ti.TICKscript, tick)
	}
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
	if !reflect.DeepEqual(vars, ti.Vars) {
		t.Fatalf("unexpected vars\ngot\n%s\nexp\n%s\n", ti.Vars, vars)
	}
}
func TestServer_UpdateTemplateID_WithTasks_Fail(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTemplateID"
	ttype := client.StreamTask
	tick := `var x = 5

stream
    |from()
        .measurement('test')
`
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}

	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         id,
		Type:       ttype,
		TICKscript: tick,
	})
	if err != nil {
		t.Fatal(err)
	}

	count := 100
	tasks := make([]client.Task, count)
	for i := 0; i < count; i++ {
		task, err := cli.CreateTask(client.CreateTaskOptions{
			TemplateID: template.ID,
			DBRPs:      dbrps,
			Status:     client.Enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
		tasks[i] = task
	}

	// Create conflicting template
	newID := "newTemplateID"
	if _, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         newID,
		Type:       ttype,
		TICKscript: tick,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = cli.UpdateTemplate(template.Link, client.UpdateTemplateOptions{
		ID:         newID,
		TICKscript: "stream",
	}); err == nil {
		t.Fatal("expected update template to fail on conflicting name")
	}

	for _, task := range tasks {
		got, err := cli.Task(task.Link, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.TemplateID != id {
			t.Errorf("unexpected task TemplateID got %s exp %s", got.TemplateID, id)
		}
		if got.TICKscript != tick {
			t.Errorf("unexpected task TICKscript got %s exp %s", got.TICKscript, tick)
		}
	}
}

func TestServer_DeleteTemplate(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTemplateID"
	ttype := client.StreamTask
	tick := `stream
    |from()
        .measurement('test')
`
	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         id,
		Type:       ttype,
		TICKscript: tick,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = cli.DeleteTemplate(template.Link)
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Template(template.Link, nil)
	if err == nil {
		t.Fatal("unexpected template:", ti)
	}
}

func TestServer_CreateTaskFromTemplate(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTemplateID"
	ttype := client.StreamTask
	tick := `// Configurable measurement
var measurement = 'test'

stream
    |from()
        .measurement(measurement)
`
	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         id,
		Type:       ttype,
		TICKscript: tick,
	})
	if err != nil {
		t.Fatal(err)
	}

	templateInfo, err := cli.Template(template.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if templateInfo.Error != "" {
		t.Fatal(templateInfo.Error)
	}
	if templateInfo.ID != id {
		t.Fatalf("unexpected template.id got %s exp %s", templateInfo.ID, id)
	}
	if templateInfo.Type != client.StreamTask {
		t.Fatalf("unexpected template.type got %v exp %v", templateInfo.Type, client.StreamTask)
	}
	if templateInfo.TICKscript != tick {
		t.Fatalf("unexpected template.TICKscript got %s exp %s", templateInfo.TICKscript, tick)
	}
	dot := "digraph testTemplateID {\nstream0 -> from1;\n}"
	if templateInfo.Dot != dot {
		t.Fatalf("unexpected template.dot\ngot\n%s\nexp\n%s\n", templateInfo.Dot, dot)
	}
	expVars := client.Vars{
		"measurement": {
			Value:       "test",
			Type:        client.VarString,
			Description: "Configurable measurement",
		},
	}
	if got, exp := templateInfo.Vars, expVars; !reflect.DeepEqual(exp, got) {
		t.Errorf("unexpected template vars: got %v exp %v", got, exp)
	}

	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	vars := client.Vars{
		"measurement": {
			Value: "another_measurement",
			Type:  client.VarString,
		},
	}

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         "taskid",
		TemplateID: id,
		DBRPs:      dbrps,
		Vars:       vars,
	})
	if err != nil {
		t.Fatal(err)
	}

	taskInfo, err := cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if taskInfo.Error != "" {
		t.Fatal(taskInfo.Error)
	}
	if taskInfo.ID != "taskid" {
		t.Fatalf("unexpected task.id got %s exp %s", taskInfo.ID, "taskid")
	}
	if taskInfo.Type != client.StreamTask {
		t.Fatalf("unexpected task.type got %v exp %v", taskInfo.Type, client.StreamTask)
	}
	if taskInfo.TICKscript != tick {
		t.Fatalf("unexpected task.TICKscript got %s exp %s", taskInfo.TICKscript, tick)
	}
	dot = "digraph taskid {\nstream0 -> from1;\n}"
	if taskInfo.Dot != dot {
		t.Fatalf("unexpected task.dot\ngot\n%s\nexp\n%s\n", taskInfo.Dot, dot)
	}
	if taskInfo.Status != client.Disabled {
		t.Fatalf("unexpected task.status got %v exp %v", taskInfo.Status, client.Disabled)
	}
	if !reflect.DeepEqual(taskInfo.DBRPs, dbrps) {
		t.Fatalf("unexpected task.dbrps got %s exp %s", taskInfo.DBRPs, dbrps)
	}
	if !reflect.DeepEqual(taskInfo.Vars, vars) {
		t.Fatalf("unexpected task.vars got %s exp %s", taskInfo.Vars, vars)
	}
}

func TestServer_StreamTask(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count('value')
    |httpOut('count')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), id)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	err = s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}

	points := `test value=1 0000000000
test value=1 0000000001
test value=1 0000000001
test value=1 0000000002
test value=1 0000000002
test value=1 0000000003
test value=1 0000000003
test value=1 0000000004
test value=1 0000000005
test value=1 0000000005
test value=1 0000000005
test value=1 0000000006
test value=1 0000000007
test value=1 0000000008
test value=1 0000000009
test value=1 0000000010
test value=1 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	exp := `{"series":[{"name":"test","columns":["time","count"],"values":[["1970-01-01T00:00:10Z",15]]}]}`
	err = s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}
}

func TestServer_StreamTask_NoRP(t *testing.T) {
	conf := NewConfig()
	conf.DefaultRetentionPolicy = "myrp"
	s := OpenServer(conf)
	defer s.Close()
	cli := Client(s)

	id := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count('value')
    |httpOut('count')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), id)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	err = s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}

	points := `test value=1 0000000000
test value=1 0000000001
test value=1 0000000001
test value=1 0000000002
test value=1 0000000002
test value=1 0000000003
test value=1 0000000003
test value=1 0000000004
test value=1 0000000005
test value=1 0000000005
test value=1 0000000005
test value=1 0000000006
test value=1 0000000007
test value=1 0000000008
test value=1 0000000009
test value=1 0000000010
test value=1 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "", points, v)

	exp := `{"series":[{"name":"test","columns":["time","count"],"values":[["1970-01-01T00:00:10Z",15]]}]}`
	err = s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}
}

func TestServer_StreamTemplateTask(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	templateId := "testStreamTemplate"
	taskId := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `
var field = 'nonexistent'
stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count(field)
    |httpOut('count')
`
	if _, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         templateId,
		Type:       ttype,
		TICKscript: tick,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         taskId,
		TemplateID: templateId,
		DBRPs:      dbrps,
		Status:     client.Enabled,
		Vars: client.Vars{
			"field": {
				Value: "value",
				Type:  client.VarString,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), taskId)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	if err := s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5); err != nil {
		t.Error(err)
	}

	points := `test value=1 0000000000
test value=1 0000000001
test value=1 0000000001
test value=1 0000000002
test value=1 0000000002
test value=1 0000000003
test value=1 0000000003
test value=1 0000000004
test value=1 0000000005
test value=1 0000000005
test value=1 0000000005
test value=1 0000000006
test value=1 0000000007
test value=1 0000000008
test value=1 0000000009
test value=1 0000000010
test value=1 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	exp := `{"series":[{"name":"test","columns":["time","count"],"values":[["1970-01-01T00:00:10Z",15]]}]}`
	if err := s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5); err != nil {
		t.Error(err)
	}
}
func TestServer_StreamTemplateTask_MissingVar(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	templateId := "testStreamTemplate"
	taskId := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `
var field string
stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count(field)
    |httpOut('count')
`
	if _, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         templateId,
		Type:       ttype,
		TICKscript: tick,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         taskId,
		TemplateID: templateId,
		DBRPs:      dbrps,
		Status:     client.Enabled,
	}); err == nil {
		t.Error("expected error for missing task vars")
	} else if exp, got := "invalid TICKscript: missing value for var \"field\".", err.Error(); got != exp {
		t.Errorf("unexpected error message: got %s exp %s", got, exp)
	}
}
func TestServer_StreamTemplateTask_AllTypes(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	templateId := "testStreamTemplate"
	taskId := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `
var bool bool
var count_threshold int
var value_threshold float
var window duration
var field string
var tagMatch regex
var match lambda
var eval lambda
var groups list
var secondGroup list
stream
    |from()
        .measurement('test')
        .where(lambda: match AND "tag" =~ tagMatch AND bool AND "value" >= value_threshold)
        .groupBy(groups)
        |log().prefix('FROM')
    |window()
        .period(window)
        .every(window)
        |log().prefix('WINDOW')
    |count(field)
        |log().prefix('COUNT')
    |groupBy(secondGroup)
    |sum('count')
        .as('count')
        |log().prefix('SUM')
    |where(lambda: "count" >= count_threshold)
        |log().prefix('WHERE')
    |eval(eval)
        .as('count')
    |httpOut('count')
`
	if _, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         templateId,
		Type:       ttype,
		TICKscript: tick,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         taskId,
		TemplateID: templateId,
		DBRPs:      dbrps,
		Status:     client.Enabled,
		Vars: client.Vars{
			"bool": {
				Value: true,
				Type:  client.VarBool,
			},
			"count_threshold": {
				Value: int64(1),
				Type:  client.VarInt,
			},
			"value_threshold": {
				Value: float64(1.0),
				Type:  client.VarFloat,
			},
			"window": {
				Value: 10 * time.Second,
				Type:  client.VarDuration,
			},
			"field": {
				Value: "value",
				Type:  client.VarString,
			},
			"tagMatch": {
				Value: "^a.*",
				Type:  client.VarRegex,
			},
			"match": {
				Value: `"value" == 1.0`,
				Type:  client.VarLambda,
			},
			"eval": {
				Value: `"count" * 2`,
				Type:  client.VarLambda,
			},
			"groups": {
				Value: []client.Var{client.Var{Type: client.VarStar}},
				Type:  client.VarList,
			},
			"secondGroup": {
				Value: []client.Var{client.Var{Value: "tag", Type: client.VarString}},
				Type:  client.VarList,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), taskId)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	if err := s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5); err != nil {
		t.Error(err)
	}

	points := `test,tag=abc,other=a value=1 0000000000
test,tag=abc,other=b value=1 0000000000
test,tag=abc,other=a value=1 0000000001
test,tag=bbc,other=b value=1 0000000001
test,tag=abc,other=a value=1 0000000002
test,tag=abc,other=a value=0 0000000002
test,tag=abc,other=b value=1 0000000003
test,tag=abc,other=a value=1 0000000003
test,tag=abc,other=a value=1 0000000004
test,tag=abc,other=b value=1 0000000005
test,tag=abc,other=a value=1 0000000005
test,tag=bbc,other=a value=1 0000000005
test,tag=abc,other=b value=1 0000000006
test,tag=abc,other=a value=1 0000000007
test,tag=abc,other=b value=0 0000000008
test,tag=abc,other=a value=1 0000000009
test,tag=abc,other=a value=1 0000000010
test,tag=abc,other=a value=1 0000000011
test,tag=abc,other=b value=1 0000000011
test,tag=bbc,other=a value=1 0000000011
test,tag=bbc,other=b value=1 0000000011
test,tag=abc,other=a value=1 0000000021
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	exp := `{"series":[{"name":"test","tags":{"tag":"abc"},"columns":["time","count"],"values":[["1970-01-01T00:00:10Z",24]]}]}`
	if err := s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5); err != nil {
		t.Error(err)
	}
}

func TestServer_StreamTemplateTaskFromUpdate(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	templateId := "testStreamTemplate"
	taskId := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `
var field = 'nonexistent'
stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count(field)
    |httpOut('count')
`
	if _, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         templateId,
		Type:       ttype,
		TICKscript: tick,
	}); err != nil {
		t.Fatal(err)
	}

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         taskId,
		TemplateID: templateId,
		DBRPs:      dbrps,
		Status:     client.Disabled,
		Vars: client.Vars{
			"field": {
				Value: "value",
				Type:  client.VarString,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	}); err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), taskId)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	if err := s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5); err != nil {
		t.Error(err)
	}

	points := `test value=1 0000000000
test value=1 0000000001
test value=1 0000000001
test value=1 0000000002
test value=1 0000000002
test value=1 0000000003
test value=1 0000000003
test value=1 0000000004
test value=1 0000000005
test value=1 0000000005
test value=1 0000000005
test value=1 0000000006
test value=1 0000000007
test value=1 0000000008
test value=1 0000000009
test value=1 0000000010
test value=1 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	exp := `{"series":[{"name":"test","columns":["time","count"],"values":[["1970-01-01T00:00:10Z",15]]}]}`
	if err := s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5); err != nil {
		t.Error(err)
	}
}
func TestServer_StreamTemplateTask_UpdateTemplate(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	templateId := "testStreamTemplate"
	taskId := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tickWrong := `
stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count('wrong')
    |httpOut('count')
`
	tickCorrect := `
var field string
stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count(field)
    |httpOut('count')
`
	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         templateId,
		Type:       ttype,
		TICKscript: tickWrong,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = cli.CreateTask(client.CreateTaskOptions{
		ID:         taskId,
		TemplateID: templateId,
		DBRPs:      dbrps,
		Status:     client.Enabled,
		Vars: client.Vars{
			"field": {
				Value: "value",
				Type:  client.VarString,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := cli.UpdateTemplate(template.Link, client.UpdateTemplateOptions{
		TICKscript: tickCorrect,
	}); err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), taskId)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	if err := s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5); err != nil {
		t.Error(err)
	}

	points := `test value=1 0000000000
test value=1 0000000001
test value=1 0000000001
test value=1 0000000002
test value=1 0000000002
test value=1 0000000003
test value=1 0000000003
test value=1 0000000004
test value=1 0000000005
test value=1 0000000005
test value=1 0000000005
test value=1 0000000006
test value=1 0000000007
test value=1 0000000008
test value=1 0000000009
test value=1 0000000010
test value=1 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	exp := `{"series":[{"name":"test","columns":["time","count"],"values":[["1970-01-01T00:00:10Z",15]]}]}`
	if err := s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5); err != nil {
		t.Error(err)
	}
}
func TestServer_StreamTemplateTask_UpdateTemplate_Rollback(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	templateId := "testStreamTemplate"
	taskId := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tickCorrect := `
var field string
stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count(field)
    |httpOut('count')
`
	tickNewVar := `
var field string
var period duration
stream
    |from()
        .measurement('test')
    |window()
        .period(period)
        .every(period)
    |count(field)
    |httpOut('count')
`
	template, err := cli.CreateTemplate(client.CreateTemplateOptions{
		ID:         templateId,
		Type:       ttype,
		TICKscript: tickCorrect,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create several tasks
	count := 5
	tasks := make([]client.Task, count)
	for i := 0; i < count; i++ {
		if task, err := cli.CreateTask(client.CreateTaskOptions{
			ID:         fmt.Sprintf("%s-%d", taskId, i),
			TemplateID: templateId,
			DBRPs:      dbrps,
			Status:     client.Enabled,
			Vars: client.Vars{
				"field": {
					Value: "value",
					Type:  client.VarString,
				},
			},
		}); err != nil {
			t.Fatal(err)
		} else {
			tasks[i] = task
		}
	}

	if _, err := cli.UpdateTemplate(template.Link, client.UpdateTemplateOptions{
		TICKscript: tickNewVar,
	}); err == nil {
		t.Error("expected error for breaking template update, got nil")
	} else if got, exp := err.Error(), `error reloading associated task testStreamTask-0: missing value for var "period".`; exp != got {
		t.Errorf("unexpected error for breaking template update, got %s exp %s", got, exp)
	}

	// Get all tasks and make sure their TICKscript has the original value
	for _, task := range tasks {
		if gotTask, err := cli.Task(task.Link, &client.TaskOptions{ScriptFormat: "raw"}); err != nil {
			t.Fatal(err)
		} else if got, exp := gotTask.TICKscript, tickCorrect; got != exp {
			t.Errorf("unexpected task TICKscript:\ngot\n%s\nexp\n%s\n", got, exp)
		}
	}

	// Update all tasks with new var
	for _, task := range tasks {
		if _, err := cli.UpdateTask(task.Link, client.UpdateTaskOptions{
			Vars: client.Vars{
				"field": {
					Value: "value",
					Type:  client.VarString,
				},
				"period": {
					Value: 10 * time.Second,
					Type:  client.VarDuration,
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Now update template should succeed since the tasks are updated too.
	if _, err := cli.UpdateTemplate(template.Link, client.UpdateTemplateOptions{
		TICKscript: tickNewVar,
	}); err != nil {
		t.Fatal(err)
	}

	for _, task := range tasks {
		taskId := task.ID
		endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), taskId)

		// Request data before any writes and expect null responses
		nullResponse := `{}`
		if err := s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5); err != nil {
			t.Error(err)
		}
	}

	points := `test value=1 0000000000
test value=1 0000000001
test value=1 0000000001
test value=1 0000000002
test value=1 0000000002
test value=1 0000000003
test value=1 0000000003
test value=1 0000000004
test value=1 0000000005
test value=1 0000000005
test value=1 0000000005
test value=1 0000000006
test value=1 0000000007
test value=1 0000000008
test value=1 0000000009
test value=1 0000000010
test value=1 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	for _, task := range tasks {
		taskId := task.ID
		endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), taskId)

		exp := `{"series":[{"name":"test","columns":["time","count"],"values":[["1970-01-01T00:00:10Z",15]]}]}`
		if err := s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5); err != nil {
			t.Error(err)
		}
	}
}

func TestServer_UpdateTaskID(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Disabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Disabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	dot := "digraph testTaskID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}

	newID := "newTaskID"
	task, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		ID: newID,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, exp := task.Link.Href, "/kapacitor/v1/tasks/newTaskID"; got != exp {
		t.Fatalf("unexpected task link got %s exp %s", got, exp)
	}

	ti, err = cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != newID {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, newID)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Disabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Disabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	dot = "digraph newTaskID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
}
func TestServer_UpdateTaskID_Fail(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	newID := "anotherTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Disabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Disabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	dot := "digraph testTaskID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}

	// Create conflicting task
	if _, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         newID,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		ID: newID,
	}); err == nil {
		t.Fatal("expected error on name conflict")
	}

	// Can still get old task
	ti, err = cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Disabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Disabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
}
func TestServer_UpdateTaskID_Enabled(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testTaskID"
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
		{
			Database:        "otherdb",
			RetentionPolicy: "default",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	ti, err := cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Enabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Enabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	if !ti.Executing {
		t.Fatal("expected task to be executing")
	}

	newID := "newTaskID"
	task, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		ID: newID,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got, exp := task.Link.Href, "/kapacitor/v1/tasks/newTaskID"; got != exp {
		t.Fatalf("unexpected task link got %s exp %s", got, exp)
	}

	ti, err = cli.Task(task.Link, nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != newID {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, newID)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Enabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Enabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	if !ti.Executing {
		t.Fatal("expected task to be executing")
	}
}

func TestServer_StreamTask_AllMeasurements(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `stream
    |from()
    |window()
        .period(10s)
        .every(10s)
    |count('value')
    |httpOut('count')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), id)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	err = s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}

	points := `test0 value=1 0000000000
test1 value=1 0000000001
test0 value=1 0000000001
test1 value=1 0000000002
test0 value=1 0000000002
test1 value=1 0000000003
test0 value=1 0000000003
test1 value=1 0000000004
test0 value=1 0000000005
test1 value=1 0000000005
test0 value=1 0000000005
test1 value=1 0000000006
test0 value=1 0000000007
test1 value=1 0000000008
test0 value=1 0000000009
test1 value=1 0000000010
test0 value=1 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	exp := `{"series":[{"name":"test0","columns":["time","count"],"values":[["1970-01-01T00:00:10Z",15]]}]}`
	err = s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}
}

func TestServer_BatchTask(t *testing.T) {
	c := NewConfig()
	c.InfluxDB[0].Enabled = true
	count := 0
	stopTimeC := make(chan time.Time, 1)

	db := NewInfluxDB(func(q string) *iclient.Response {
		stmt, err := influxql.ParseStatement(q)
		if err != nil {
			return &iclient.Response{Err: err.Error()}
		}
		slct, ok := stmt.(*influxql.SelectStatement)
		if !ok {
			return nil
		}
		cond, ok := slct.Condition.(*influxql.BinaryExpr)
		if !ok {
			return &iclient.Response{Err: "expected select condition to be binary expression"}
		}
		stopTimeExpr, ok := cond.RHS.(*influxql.BinaryExpr)
		if !ok {
			return &iclient.Response{Err: "expected select condition rhs to be binary expression"}
		}
		stopTL, ok := stopTimeExpr.RHS.(*influxql.StringLiteral)
		if !ok {
			return &iclient.Response{Err: "expected select condition rhs to be string literal"}
		}
		count++
		switch count {
		case 1:
			stopTime, err := time.Parse(time.RFC3339Nano, stopTL.Val)
			if err != nil {
				return &iclient.Response{Err: err.Error()}
			}
			stopTimeC <- stopTime
			return &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								stopTime.Add(-2 * time.Millisecond).Format(time.RFC3339Nano),
								1.0,
							},
							{
								stopTime.Add(-1 * time.Millisecond).Format(time.RFC3339Nano),
								1.0,
							},
						},
					}},
				}},
			}
		default:
			return &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values:  [][]interface{}{},
					}},
				}},
			}
		}
	})
	c.InfluxDB[0].URLs = []string{db.URL()}
	s := OpenServer(c)
	defer s.Close()
	cli := Client(s)

	id := "testBatchTask"
	ttype := client.BatchTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `batch
    |query('SELECT value from mydb.myrp.cpu')
        .period(5ms)
        .every(5ms)
        .align()
    |count('value')
    |where(lambda: "count" == 2)
    |httpOut('count')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), id)

	timeout := time.NewTicker(100 * time.Millisecond)
	defer timeout.Stop()
	select {
	case <-timeout.C:
		t.Fatal("timedout waiting for query")
	case stopTime := <-stopTimeC:
		exp := fmt.Sprintf(`{"series":[{"name":"cpu","columns":["time","count"],"values":[["%s",2]]}]}`, stopTime.Local().Format(time.RFC3339Nano))
		err = s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5)
		if err != nil {
			t.Error(err)
		}
		_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
			Status: client.Disabled,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestServer_BatchTask_InfluxDBConfigUpdate(t *testing.T) {
	c := NewConfig()
	c.InfluxDB[0].Enabled = true
	count := 0
	stopTimeC := make(chan time.Time, 1)

	badCount := 0

	dbBad := NewInfluxDB(func(q string) *iclient.Response {
		badCount++
		// Return empty results
		return &iclient.Response{
			Results: []iclient.Result{},
		}
	})
	defer dbBad.Close()
	db := NewInfluxDB(func(q string) *iclient.Response {
		stmt, err := influxql.ParseStatement(q)
		if err != nil {
			return &iclient.Response{Err: err.Error()}
		}
		slct, ok := stmt.(*influxql.SelectStatement)
		if !ok {
			return nil
		}
		cond, ok := slct.Condition.(*influxql.BinaryExpr)
		if !ok {
			return &iclient.Response{Err: "expected select condition to be binary expression"}
		}
		stopTimeExpr, ok := cond.RHS.(*influxql.BinaryExpr)
		if !ok {
			return &iclient.Response{Err: "expected select condition rhs to be binary expression"}
		}
		stopTL, ok := stopTimeExpr.RHS.(*influxql.StringLiteral)
		if !ok {
			return &iclient.Response{Err: "expected select condition rhs to be string literal"}
		}
		count++
		switch count {
		case 1:
			stopTime, err := time.Parse(time.RFC3339Nano, stopTL.Val)
			if err != nil {
				return &iclient.Response{Err: err.Error()}
			}
			stopTimeC <- stopTime
			return &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								stopTime.Add(-2 * time.Millisecond).Format(time.RFC3339Nano),
								1.0,
							},
							{
								stopTime.Add(-1 * time.Millisecond).Format(time.RFC3339Nano),
								1.0,
							},
						},
					}},
				}},
			}
		default:
			return &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values:  [][]interface{}{},
					}},
				}},
			}
		}
	})
	defer db.Close()

	// Set bad URL first
	c.InfluxDB[0].URLs = []string{dbBad.URL()}
	s := OpenServer(c)
	defer s.Close()
	cli := Client(s)

	id := "testBatchTask"
	ttype := client.BatchTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `batch
    |query('SELECT value from mydb.myrp.cpu')
        .period(5ms)
        .every(5ms)
        .align()
    |count('value')
    |where(lambda: "count" == 2)
    |httpOut('count')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Update InfluxDB config, while task is running
	influxdbDefault := cli.ConfigElementLink("influxdb", "default")
	if err := cli.ConfigUpdate(influxdbDefault, client.ConfigUpdateAction{
		Set: map[string]interface{}{
			"urls": []string{db.URL()},
		},
	}); err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), id)
	timeout := time.NewTicker(100 * time.Millisecond)
	defer timeout.Stop()
	select {
	case <-timeout.C:
		t.Fatal("timedout waiting for query")
	case stopTime := <-stopTimeC:
		exp := fmt.Sprintf(`{"series":[{"name":"cpu","columns":["time","count"],"values":[["%s",2]]}]}`, stopTime.Local().Format(time.RFC3339Nano))
		err = s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5)
		if err != nil {
			t.Error(err)
		}
		_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
			Status: client.Disabled,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if badCount == 0 {
		t.Error("expected bad influxdb to be queried at least once")
	}
}

func TestServer_InvalidBatchTask(t *testing.T) {
	c := NewConfig()
	c.InfluxDB[0].Enabled = true
	db := NewInfluxDB(func(q string) *iclient.Response {
		return nil
	})
	c.InfluxDB[0].URLs = []string{db.URL()}
	s := OpenServer(c)
	defer s.Close()
	cli := Client(s)

	id := "testInvalidBatchTask"
	ttype := client.BatchTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `batch
    |query(' SELECT value from unknowndb.unknownrp.cpu ')
        .period(5ms)
        .every(5ms)
    |count('value')
    |httpOut('count')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	expErr := `batch query is not allowed to request data from "unknowndb"."unknownrp"`
	if err != nil && err.Error() != expErr {
		t.Fatalf("unexpected err: got %v exp %s", err, expErr)
	}

	err = cli.DeleteTask(task.Link)
	if err != nil {
		t.Fatal(err)
	}
}

func TestServer_RecordReplayStream(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	id := "testStreamTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}

	tmpDir, err := ioutil.TempDir("", "testStreamTaskRecording")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	tick := `stream
    |from()
        .measurement('test')
    |window()
        .period(10s)
        .every(10s)
    |count('value')
    |alert()
        .id('test-count')
        .message('{{ .ID }} got: {{ index .Fields "count" }}')
        .crit(lambda: TRUE)
        .log('` + tmpDir + `/alert.log')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	recording, err := cli.RecordStream(client.RecordStreamOptions{
		ID:   "recordingid",
		Task: task.ID,
		Stop: time.Date(1970, 1, 1, 0, 0, 10, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := "/kapacitor/v1/recordings/recordingid", recording.Link.Href; exp != got {
		t.Errorf("unexpected recording.Link.Href got %s exp %s", got, exp)
	}

	points := `test value=1 0000000000
test value=1 0000000001
test value=1 0000000001
test value=1 0000000002
test value=1 0000000002
test value=1 0000000003
test value=1 0000000003
test value=1 0000000004
test value=1 0000000005
test value=1 0000000005
test value=1 0000000005
test value=1 0000000006
test value=1 0000000007
test value=1 0000000008
test value=1 0000000009
test value=1 0000000010
test value=1 0000000011
test value=1 0000000012
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	retry := 0
	for recording.Status == client.Running {
		time.Sleep(100 * time.Millisecond)
		recording, err = cli.Recording(recording.Link)
		if err != nil {
			t.Fatal(err)
		}
		retry++
		if retry > 10 {
			t.Fatal("failed to finish recording")
		}
	}
	if recording.Status != client.Finished || recording.Error != "" {
		t.Errorf("recording failed: %s", recording.Error)
	}

	replay, err := cli.CreateReplay(client.CreateReplayOptions{
		ID:            "replayid",
		Task:          id,
		Recording:     recording.ID,
		Clock:         client.Fast,
		RecordingTime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := "/kapacitor/v1/replays/replayid", replay.Link.Href; exp != got {
		t.Errorf("unexpected replay.Link.Href got %s exp %s", got, exp)
	}
	if exp, got := id, replay.Task; exp != got {
		t.Errorf("unexpected replay.Task got %s exp %s", got, exp)
	}

	retry = 0
	for replay.Status == client.Running {
		time.Sleep(100 * time.Millisecond)
		replay, err = cli.Replay(replay.Link)
		if err != nil {
			t.Fatal(err)
		}
		retry++
		if retry > 10 {
			t.Fatal("failed to finish replay")
		}
	}
	if replay.Status != client.Finished || replay.Error != "" {
		t.Errorf("replay failed: %s", replay.Error)
	}

	f, err := os.Open(path.Join(tmpDir, "alert.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type response struct {
		ID      string          `json:"id"`
		Message string          `json:"message"`
		Time    time.Time       `json:"time"`
		Level   string          `json:"level"`
		Data    influxql.Result `json:"data"`
	}
	exp := response{
		ID:      "test-count",
		Message: "test-count got: 15",
		Time:    time.Date(1970, 1, 1, 0, 0, 10, 0, time.UTC),
		Level:   "CRITICAL",
		Data: influxql.Result{
			Series: models.Rows{
				{
					Name:    "test",
					Columns: []string{"time", "count"},
					Values: [][]interface{}{
						{
							time.Date(1970, 1, 1, 0, 0, 10, 0, time.UTC).Format(time.RFC3339Nano),
							15.0,
						},
					},
				},
			},
		},
	}
	got := response{}
	d := json.NewDecoder(f)
	d.Decode(&got)
	if !reflect.DeepEqual(exp, got) {
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got, exp)
	}

	recordings, err := cli.ListRecordings(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 1, len(recordings); exp != got {
		t.Fatalf("unexpected recordings list:\ngot %v\nexp %v\nrecordings %v", got, exp, recordings)
	}

	err = cli.DeleteRecording(recordings[0].Link)
	if err != nil {
		t.Error(err)
	}

	recordings, err = cli.ListRecordings(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(recordings); exp != got {
		t.Errorf("unexpected recordings list after delete:\ngot %v\nexp %v\nrecordings %v", got, exp, recordings)
	}

	replays, err := cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 1, len(replays); exp != got {
		t.Fatalf("unexpected replays list:\ngot %v\nexp %v\nreplays %v", got, exp, replays)
	}

	err = cli.DeleteReplay(replays[0].Link)
	if err != nil {
		t.Error(err)
	}

	replays, err = cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(replays); exp != got {
		t.Errorf("unexpected replays list after delete:\ngot %v\nexp %v\nreplays %v", got, exp, replays)
	}
}

func TestServer_RecordReplayBatch(t *testing.T) {
	c := NewConfig()
	c.InfluxDB[0].Enabled = true
	value := 0
	db := NewInfluxDB(func(q string) *iclient.Response {
		if len(q) > 6 && q[:6] == "SELECT" {
			r := &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, value, 0, time.UTC).Format(time.RFC3339Nano),
								float64(value),
							},
							{
								time.Date(1971, 1, 1, 0, 0, value+1, 0, time.UTC).Format(time.RFC3339Nano),
								float64(value + 1),
							},
						},
					}},
				}},
			}
			value += 2
			return r
		}
		return nil
	})
	c.InfluxDB[0].URLs = []string{db.URL()}
	s := OpenServer(c)
	defer s.Close()
	cli := Client(s)

	id := "testBatchTask"
	ttype := client.BatchTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}

	tmpDir, err := ioutil.TempDir("", "testBatchTaskRecording")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	tick := `batch
    |query('SELECT value from mydb.myrp.cpu')
        .period(2s)
        .every(2s)
    |alert()
        .id('test-batch')
        .message('{{ .ID }} got: {{ index .Fields "value" }}')
        .crit(lambda: "value" > 2.0)
        .log('` + tmpDir + `/alert.log')
`

	_, err = cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	recording, err := cli.RecordBatch(client.RecordBatchOptions{
		ID:    "recordingid",
		Task:  id,
		Start: time.Date(1971, 1, 1, 0, 0, 0, 0, time.UTC),
		Stop:  time.Date(1971, 1, 1, 0, 0, 6, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := "/kapacitor/v1/recordings/recordingid", recording.Link.Href; exp != got {
		t.Errorf("unexpected recording.Link.Href got %s exp %s", got, exp)
	}
	// Wait for recording to finish.
	retry := 0
	for recording.Status == client.Running {
		time.Sleep(100 * time.Millisecond)
		recording, err = cli.Recording(recording.Link)
		if err != nil {
			t.Fatal(err)
		}
		retry++
		if retry > 10 {
			t.Fatal("failed to perfom recording")
		}
	}

	replay, err := cli.CreateReplay(client.CreateReplayOptions{
		Task:          id,
		Recording:     recording.ID,
		Clock:         client.Fast,
		RecordingTime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := id, replay.Task; exp != got {
		t.Errorf("unexpected replay.Task got %s exp %s", got, exp)
	}

	// Wait for replay to finish.
	retry = 0
	for replay.Status == client.Running {
		time.Sleep(100 * time.Millisecond)
		replay, err = cli.Replay(replay.Link)
		if err != nil {
			t.Fatal(err)
		}
		retry++
		if retry > 10 {
			t.Fatal("failed to perform replay")
		}
	}

	f, err := os.Open(path.Join(tmpDir, "alert.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type response struct {
		ID      string          `json:"id"`
		Message string          `json:"message"`
		Time    time.Time       `json:"time"`
		Level   string          `json:"level"`
		Data    influxql.Result `json:"data"`
	}
	exp := []response{
		{
			ID:      "test-batch",
			Message: "test-batch got: 3",
			Time:    time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC),
			Level:   "CRITICAL",
			Data: influxql.Result{
				Series: models.Rows{
					{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, 2, 0, time.UTC).Format(time.RFC3339Nano),
								2.0,
							},
							{
								time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC).Format(time.RFC3339Nano),
								3.0,
							},
						},
					},
				},
			},
		},
		{
			ID:      "test-batch",
			Message: "test-batch got: 4",
			Time:    time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC),
			Level:   "CRITICAL",
			Data: influxql.Result{
				Series: models.Rows{
					{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC).Format(time.RFC3339Nano),
								4.0,
							},
							{
								time.Date(1971, 1, 1, 0, 0, 5, 0, time.UTC).Format(time.RFC3339Nano),
								5.0,
							},
						},
					},
				},
			},
		},
	}
	dec := json.NewDecoder(f)
	got := make([]response, 0)
	for dec.More() {
		g := response{}
		dec.Decode(&g)
		got = append(got, g)
	}
	if !reflect.DeepEqual(exp, got) {
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got, exp)
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got[0].Data.Series[0], exp[0].Data.Series[0])
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got[1].Data.Series[0], exp[1].Data.Series[0])
	}

	recordings, err := cli.ListRecordings(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 1, len(recordings); exp != got {
		t.Fatalf("unexpected recordings list:\ngot %v\nexp %v", got, exp)
	}

	err = cli.DeleteRecording(recordings[0].Link)
	if err != nil {
		t.Error(err)
	}

	recordings, err = cli.ListRecordings(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(recordings); exp != got {
		t.Errorf("unexpected recordings list:\ngot %v\nexp %v", got, exp)
	}

	replays, err := cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 1, len(replays); exp != got {
		t.Fatalf("unexpected replays list:\ngot %v\nexp %v", got, exp)
	}

	err = cli.DeleteReplay(replays[0].Link)
	if err != nil {
		t.Error(err)
	}

	replays, err = cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(replays); exp != got {
		t.Errorf("unexpected replays list:\ngot %v\nexp %v", got, exp)
	}
}
func TestServer_ReplayBatch(t *testing.T) {
	c := NewConfig()
	c.InfluxDB[0].Enabled = true
	value := 0
	db := NewInfluxDB(func(q string) *iclient.Response {
		if len(q) > 6 && q[:6] == "SELECT" {
			r := &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, value, 0, time.UTC).Format(time.RFC3339Nano),
								float64(value),
							},
							{
								time.Date(1971, 1, 1, 0, 0, value+1, 0, time.UTC).Format(time.RFC3339Nano),
								float64(value + 1),
							},
						},
					}},
				}},
			}
			value += 2
			return r
		}
		return nil
	})
	c.InfluxDB[0].URLs = []string{db.URL()}
	s := OpenServer(c)
	defer s.Close()
	cli := Client(s)

	id := "testBatchTask"
	ttype := client.BatchTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}

	tmpDir, err := ioutil.TempDir("", "testBatchTaskRecording")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	tick := `batch
    |query('SELECT value from mydb.myrp.cpu')
        .period(2s)
        .every(2s)
    |alert()
        .id('test-batch')
        .message('{{ .ID }} got: {{ index .Fields "value" }}')
        .crit(lambda: "value" > 2.0)
        .log('` + tmpDir + `/alert.log')
`

	_, err = cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	replay, err := cli.ReplayBatch(client.ReplayBatchOptions{
		ID:            "replayid",
		Task:          id,
		Start:         time.Date(1971, 1, 1, 0, 0, 0, 0, time.UTC),
		Stop:          time.Date(1971, 1, 1, 0, 0, 6, 0, time.UTC),
		Clock:         client.Fast,
		RecordingTime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := "/kapacitor/v1/replays/replayid", replay.Link.Href; exp != got {
		t.Errorf("unexpected replay.Link.Href got %s exp %s", got, exp)
	}
	// Wait for replay to finish.
	retry := 0
	for replay.Status == client.Running {
		time.Sleep(100 * time.Millisecond)
		replay, err = cli.Replay(replay.Link)
		if err != nil {
			t.Fatal(err)
		}
		retry++
		if retry > 10 {
			t.Fatal("failed to perfom replay")
		}
	}

	f, err := os.Open(path.Join(tmpDir, "alert.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type response struct {
		ID      string          `json:"id"`
		Message string          `json:"message"`
		Time    time.Time       `json:"time"`
		Level   string          `json:"level"`
		Data    influxql.Result `json:"data"`
	}
	exp := []response{
		{
			ID:      "test-batch",
			Message: "test-batch got: 3",
			Time:    time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC),
			Level:   "CRITICAL",
			Data: influxql.Result{
				Series: models.Rows{
					{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, 2, 0, time.UTC).Format(time.RFC3339Nano),
								2.0,
							},
							{
								time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC).Format(time.RFC3339Nano),
								3.0,
							},
						},
					},
				},
			},
		},
		{
			ID:      "test-batch",
			Message: "test-batch got: 4",
			Time:    time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC),
			Level:   "CRITICAL",
			Data: influxql.Result{
				Series: models.Rows{
					{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC).Format(time.RFC3339Nano),
								4.0,
							},
							{
								time.Date(1971, 1, 1, 0, 0, 5, 0, time.UTC).Format(time.RFC3339Nano),
								5.0,
							},
						},
					},
				},
			},
		},
	}
	dec := json.NewDecoder(f)
	got := make([]response, 0)
	for dec.More() {
		g := response{}
		dec.Decode(&g)
		got = append(got, g)
	}
	if !reflect.DeepEqual(exp, got) {
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got, exp)
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got[0].Data.Series[0], exp[0].Data.Series[0])
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got[1].Data.Series[0], exp[1].Data.Series[0])
	}

	recordings, err := cli.ListRecordings(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(recordings); exp != got {
		t.Fatalf("unexpected recordings list:\ngot %v\nexp %v", got, exp)
	}

	replays, err := cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 1, len(replays); exp != got {
		t.Fatalf("unexpected replays list:\ngot %v\nexp %v", got, exp)
	}

	err = cli.DeleteReplay(replays[0].Link)
	if err != nil {
		t.Error(err)
	}

	replays, err = cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(replays); exp != got {
		t.Errorf("unexpected replays list:\ngot %v\nexp %v", got, exp)
	}
}

func TestServer_RecordReplayQuery(t *testing.T) {
	c := NewConfig()
	c.InfluxDB[0].Enabled = true
	db := NewInfluxDB(func(q string) *iclient.Response {
		if len(q) > 6 && q[:6] == "SELECT" {
			r := &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{
						{
							Name:    "cpu",
							Columns: []string{"time", "value"},
							Values: [][]interface{}{
								{
									time.Date(1971, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
									0.0,
								},
								{
									time.Date(1971, 1, 1, 0, 0, 1, 0, time.UTC).Format(time.RFC3339Nano),
									1.0,
								},
							},
						},
						{
							Name:    "cpu",
							Columns: []string{"time", "value"},
							Values: [][]interface{}{
								{
									time.Date(1971, 1, 1, 0, 0, 2, 0, time.UTC).Format(time.RFC3339Nano),
									2.0,
								},
								{
									time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC).Format(time.RFC3339Nano),
									3.0,
								},
							},
						},
						{
							Name:    "cpu",
							Columns: []string{"time", "value"},
							Values: [][]interface{}{
								{
									time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC).Format(time.RFC3339Nano),
									4.0,
								},
								{
									time.Date(1971, 1, 1, 0, 0, 5, 0, time.UTC).Format(time.RFC3339Nano),
									5.0,
								},
							},
						},
					},
				}},
			}
			return r
		}
		return nil
	})
	c.InfluxDB[0].URLs = []string{db.URL()}
	s := OpenServer(c)
	defer s.Close()
	cli := Client(s)

	id := "testBatchTask"
	ttype := client.BatchTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}

	tmpDir, err := ioutil.TempDir("", "testBatchTaskRecording")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	tick := `batch
    |query('SELECT value from mydb.myrp.cpu')
        .period(2s)
        .every(2s)
    |alert()
        .id('test-batch')
        .message('{{ .ID }} got: {{ index .Fields "value" }}')
        .crit(lambda: "value" > 2.0)
        .log('` + tmpDir + `/alert.log')
`

	_, err = cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	recording, err := cli.RecordQuery(client.RecordQueryOptions{
		ID:    "recordingid",
		Query: "SELECT value from mydb.myrp.cpu",
		Type:  client.BatchTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := "/kapacitor/v1/recordings/recordingid", recording.Link.Href; exp != got {
		t.Errorf("unexpected recording.Link.Href got %s exp %s", got, exp)
	}
	// Wait for recording to finish.
	retry := 0
	for recording.Status == client.Running {
		time.Sleep(100 * time.Millisecond)
		recording, err = cli.Recording(recording.Link)
		if err != nil {
			t.Fatal(err)
		}
		retry++
		if retry > 10 {
			t.Fatal("failed to perfom recording")
		}
	}

	replay, err := cli.CreateReplay(client.CreateReplayOptions{
		Task:          id,
		Recording:     recording.ID,
		Clock:         client.Fast,
		RecordingTime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := id, replay.Task; exp != got {
		t.Errorf("unexpected replay.Task got %s exp %s", got, exp)
	}

	// Wait for replay to finish.
	retry = 0
	for replay.Status == client.Running {
		time.Sleep(100 * time.Millisecond)
		replay, err = cli.Replay(replay.Link)
		if err != nil {
			t.Fatal(err)
		}
		retry++
		if retry > 10 {
			t.Fatal("failed to perfom replay")
		}
	}

	f, err := os.Open(path.Join(tmpDir, "alert.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type response struct {
		ID      string          `json:"id"`
		Message string          `json:"message"`
		Time    time.Time       `json:"time"`
		Level   string          `json:"level"`
		Data    influxql.Result `json:"data"`
	}
	exp := []response{
		{
			ID:      "test-batch",
			Message: "test-batch got: 3",
			Time:    time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC),
			Level:   "CRITICAL",
			Data: influxql.Result{
				Series: models.Rows{
					{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, 2, 0, time.UTC).Format(time.RFC3339Nano),
								2.0,
							},
							{
								time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC).Format(time.RFC3339Nano),
								3.0,
							},
						},
					},
				},
			},
		},
		{
			ID:      "test-batch",
			Message: "test-batch got: 4",
			Time:    time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC),
			Level:   "CRITICAL",
			Data: influxql.Result{
				Series: models.Rows{
					{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC).Format(time.RFC3339Nano),
								4.0,
							},
							{
								time.Date(1971, 1, 1, 0, 0, 5, 0, time.UTC).Format(time.RFC3339Nano),
								5.0,
							},
						},
					},
				},
			},
		},
	}
	dec := json.NewDecoder(f)
	got := make([]response, 0)
	for dec.More() {
		g := response{}
		dec.Decode(&g)
		got = append(got, g)
	}
	if !reflect.DeepEqual(exp, got) {
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got, exp)
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got[0].Data.Series[0], exp[0].Data.Series[0])
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got[1].Data.Series[0], exp[1].Data.Series[0])
	}

	// ------------
	// Test List/Delete Recordings/Replays

	recordings, err := cli.ListRecordings(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 1, len(recordings); exp != got {
		t.Fatalf("unexpected recordings list:\ngot %v\nexp %v", got, exp)
	}

	// Test List Recordings via direct default URL
	resp, err := http.Get(s.URL() + "/recordings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if exp, got := http.StatusOK, resp.StatusCode; exp != got {
		t.Errorf("unexpected status code, got %d exp %d", got, exp)
	}
	// Response type
	type recResponse struct {
		Recordings []client.Recording `json:"recordings"`
	}
	dec = json.NewDecoder(resp.Body)
	recR := recResponse{}
	dec.Decode(&recR)
	if exp, got := 1, len(recR.Recordings); exp != got {
		t.Fatalf("unexpected recordings count, got %d exp %d", got, exp)
	}

	err = cli.DeleteRecording(recordings[0].Link)
	if err != nil {
		t.Error(err)
	}

	recordings, err = cli.ListRecordings(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(recordings); exp != got {
		t.Errorf("unexpected recordings list:\ngot %v\nexp %v", got, exp)
	}

	replays, err := cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 1, len(replays); exp != got {
		t.Fatalf("unexpected replays list:\ngot %v\nexp %v", got, exp)
	}

	// Test List Replays via direct default URL
	resp, err = http.Get(s.URL() + "/replays")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if exp, got := http.StatusOK, resp.StatusCode; exp != got {
		t.Errorf("unexpected status code, got %d exp %d", got, exp)
	}
	// Response type
	type repResponse struct {
		Replays []client.Replay `json:"replays"`
	}
	dec = json.NewDecoder(resp.Body)
	repR := repResponse{}
	dec.Decode(&repR)
	if exp, got := 1, len(repR.Replays); exp != got {
		t.Fatalf("unexpected replays count, got %d exp %d", got, exp)
	}

	err = cli.DeleteReplay(replays[0].Link)
	if err != nil {
		t.Error(err)
	}

	replays, err = cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(replays); exp != got {
		t.Errorf("unexpected replays list:\ngot %v\nexp %v", got, exp)
	}
}

func TestServer_ReplayQuery(t *testing.T) {
	c := NewConfig()
	c.InfluxDB[0].Enabled = true
	db := NewInfluxDB(func(q string) *iclient.Response {
		if len(q) > 6 && q[:6] == "SELECT" {
			r := &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{
						{
							Name:    "cpu",
							Columns: []string{"time", "value"},
							Values: [][]interface{}{
								{
									time.Date(1971, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
									0.0,
								},
								{
									time.Date(1971, 1, 1, 0, 0, 1, 0, time.UTC).Format(time.RFC3339Nano),
									1.0,
								},
							},
						},
						{
							Name:    "cpu",
							Columns: []string{"time", "value"},
							Values: [][]interface{}{
								{
									time.Date(1971, 1, 1, 0, 0, 2, 0, time.UTC).Format(time.RFC3339Nano),
									2.0,
								},
								{
									time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC).Format(time.RFC3339Nano),
									3.0,
								},
							},
						},
						{
							Name:    "cpu",
							Columns: []string{"time", "value"},
							Values: [][]interface{}{
								{
									time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC).Format(time.RFC3339Nano),
									4.0,
								},
								{
									time.Date(1971, 1, 1, 0, 0, 5, 0, time.UTC).Format(time.RFC3339Nano),
									5.0,
								},
							},
						},
					},
				}},
			}
			return r
		}
		return nil
	})
	c.InfluxDB[0].URLs = []string{db.URL()}
	s := OpenServer(c)
	defer s.Close()
	cli := Client(s)

	id := "testBatchTask"
	ttype := client.BatchTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}

	tmpDir, err := ioutil.TempDir("", "testBatchTaskRecording")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	tick := `batch
    |query('SELECT value from mydb.myrp.cpu')
        .period(2s)
        .every(2s)
    |alert()
        .id('test-batch')
        .message('{{ .ID }} got: {{ index .Fields "value" }}')
        .crit(lambda: "value" > 2.0)
        .log('` + tmpDir + `/alert.log')
`

	_, err = cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	replay, err := cli.ReplayQuery(client.ReplayQueryOptions{
		ID:            "replayid",
		Query:         "SELECT value from mydb.myrp.cpu",
		Task:          id,
		Clock:         client.Fast,
		RecordingTime: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exp, got := "/kapacitor/v1/replays/replayid", replay.Link.Href; exp != got {
		t.Errorf("unexpected replay.Link.Href got %s exp %s", got, exp)
	}
	// Wait for replay to finish.
	retry := 0
	for replay.Status == client.Running {
		time.Sleep(100 * time.Millisecond)
		replay, err = cli.Replay(replay.Link)
		if err != nil {
			t.Fatal(err)
		}
		retry++
		if retry > 10 {
			t.Fatal("failed to perfom replay")
		}
	}

	f, err := os.Open(path.Join(tmpDir, "alert.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type response struct {
		ID      string          `json:"id"`
		Message string          `json:"message"`
		Time    time.Time       `json:"time"`
		Level   string          `json:"level"`
		Data    influxql.Result `json:"data"`
	}
	exp := []response{
		{
			ID:      "test-batch",
			Message: "test-batch got: 3",
			Time:    time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC),
			Level:   "CRITICAL",
			Data: influxql.Result{
				Series: models.Rows{
					{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, 2, 0, time.UTC).Format(time.RFC3339Nano),
								2.0,
							},
							{
								time.Date(1971, 1, 1, 0, 0, 3, 0, time.UTC).Format(time.RFC3339Nano),
								3.0,
							},
						},
					},
				},
			},
		},
		{
			ID:      "test-batch",
			Message: "test-batch got: 4",
			Time:    time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC),
			Level:   "CRITICAL",
			Data: influxql.Result{
				Series: models.Rows{
					{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Values: [][]interface{}{
							{
								time.Date(1971, 1, 1, 0, 0, 4, 0, time.UTC).Format(time.RFC3339Nano),
								4.0,
							},
							{
								time.Date(1971, 1, 1, 0, 0, 5, 0, time.UTC).Format(time.RFC3339Nano),
								5.0,
							},
						},
					},
				},
			},
		},
	}
	dec := json.NewDecoder(f)
	got := make([]response, 0)
	for dec.More() {
		g := response{}
		dec.Decode(&g)
		got = append(got, g)
	}
	if !reflect.DeepEqual(exp, got) {
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got, exp)
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got[0].Data.Series[0], exp[0].Data.Series[0])
		t.Errorf("unexpected alert log:\ngot %v\nexp %v", got[1].Data.Series[0], exp[1].Data.Series[0])
	}

	recordings, err := cli.ListRecordings(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(recordings); exp != got {
		t.Fatalf("unexpected recordings list:\ngot %v\nexp %v", got, exp)
	}

	replays, err := cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 1, len(replays); exp != got {
		t.Fatalf("unexpected replays list:\ngot %v\nexp %v", got, exp)
	}

	err = cli.DeleteReplay(replays[0].Link)
	if err != nil {
		t.Error(err)
	}

	replays, err = cli.ListReplays(nil)
	if err != nil {
		t.Error(err)
	}
	if exp, got := 0, len(replays); exp != got {
		t.Errorf("unexpected replays list:\ngot %v\nexp %v", got, exp)
	}
}

// If this test fails due to missing python dependencies, run 'INSTALL_PREFIX=/usr/local ./install-deps.sh' from the root directory of the
// kapacitor project.
func TestServer_UDFStreamAgents(t *testing.T) {
	tdir, err := ioutil.TempDir("", "kapacitor_server_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tdir)

	agents := []struct {
		buildFunc func() error
		config    udf.FunctionConfig
	}{
		// Go
		{
			buildFunc: func() error {
				// Explicitly compile the binary.
				// We could just use 'go run' but I ran into race conditions
				// where 'go run' was not handing off to the compiled process in time
				// and I didn't care to dig into 'go run's specific behavior.
				cmd := exec.Command(
					"go",
					"build",
					"-o",
					filepath.Join(tdir, "movavg"),
					filepath.Join(udfDir, "agent/examples/moving_avg/moving_avg.go"),
				)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Log(string(out))
					return err
				}
				return nil
			},
			config: udf.FunctionConfig{
				Prog:    filepath.Join(tdir, "movavg"),
				Timeout: toml.Duration(time.Minute),
			},
		},
		// Python
		{
			buildFunc: func() error { return nil },
			config: udf.FunctionConfig{
				Prog:    "python2",
				Args:    []string{"-u", filepath.Join(udfDir, "agent/examples/moving_avg/moving_avg.py")},
				Timeout: toml.Duration(time.Minute),
				Env: map[string]string{
					"PYTHONPATH": strings.Join(
						[]string{filepath.Join(udfDir, "agent/py"), os.Getenv("PYTHONPATH")},
						string(filepath.ListSeparator),
					),
				},
			},
		},
	}
	for _, agent := range agents {
		err := agent.buildFunc()
		if err != nil {
			t.Fatal(err)
		}
		c := NewConfig()
		c.UDF.Functions = map[string]udf.FunctionConfig{
			"movingAvg": agent.config,
		}
		testStreamAgent(t, c)
	}
}

func testStreamAgent(t *testing.T, c *server.Config) {
	s := NewServer(c)
	err := s.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cli := Client(s)

	id := "testUDFTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `stream
    |from()
        .measurement('test')
        .groupBy('group')
    @movingAvg()
        .field('value')
        .size(10)
        .as('mean')
    |window()
        .period(11s)
        .every(11s)
    |last('mean').as('mean')
    |httpOut('moving_avg')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/moving_avg", s.URL(), id)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	err = s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}

	points := `test,group=a value=1 0000000000
test,group=b value=2 0000000000
test,group=a value=1 0000000001
test,group=b value=2 0000000001
test,group=a value=1 0000000002
test,group=b value=2 0000000002
test,group=a value=1 0000000003
test,group=b value=2 0000000003
test,group=a value=1 0000000004
test,group=b value=2 0000000004
test,group=a value=1 0000000005
test,group=b value=2 0000000005
test,group=a value=1 0000000006
test,group=b value=2 0000000006
test,group=a value=1 0000000007
test,group=b value=2 0000000007
test,group=a value=1 0000000008
test,group=b value=2 0000000008
test,group=a value=1 0000000009
test,group=b value=2 0000000009
test,group=a value=0 0000000010
test,group=b value=1 0000000010
test,group=a value=0 0000000011
test,group=b value=0 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	exp := `{"series":[{"name":"test","tags":{"group":"a"},"columns":["time","mean"],"values":[["1970-01-01T00:00:11Z",0.9]]},{"name":"test","tags":{"group":"b"},"columns":["time","mean"],"values":[["1970-01-01T00:00:11Z",1.9]]}]}`
	err = s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}
}

// If this test fails due to missing python dependencies, run 'INSTALL_PREFIX=/usr/local ./install-deps.sh' from the root directory of the
// kapacitor project.
func TestServer_UDFStreamAgentsSocket(t *testing.T) {
	tdir, err := ioutil.TempDir("", "kapacitor_server_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tdir)

	agents := []struct {
		startFunc func() *exec.Cmd
		config    udf.FunctionConfig
	}{
		// Go
		{
			startFunc: func() *exec.Cmd {
				cmd := exec.Command(
					"go",
					"build",
					"-o",
					filepath.Join(tdir, "mirror"),
					filepath.Join(udfDir, "agent/examples/mirror/mirror.go"),
				)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatal(string(out))
				}
				cmd = exec.Command(
					filepath.Join(tdir, "mirror"),
					"-socket",
					filepath.Join(tdir, "mirror.go.sock"),
				)
				cmd.Stderr = os.Stderr
				return cmd
			},
			config: udf.FunctionConfig{
				Socket:  filepath.Join(tdir, "mirror.go.sock"),
				Timeout: toml.Duration(time.Minute),
			},
		},
		// Python
		{
			startFunc: func() *exec.Cmd {
				cmd := exec.Command(
					"python2",
					"-u",
					filepath.Join(udfDir, "agent/examples/mirror/mirror.py"),
					filepath.Join(tdir, "mirror.py.sock"),
				)
				cmd.Stderr = os.Stderr
				env := os.Environ()
				env = append(env, fmt.Sprintf(
					"%s=%s",
					"PYTHONPATH",
					strings.Join(
						[]string{filepath.Join(udfDir, "agent/py"), os.Getenv("PYTHONPATH")},
						string(filepath.ListSeparator),
					),
				))
				cmd.Env = env
				return cmd
			},
			config: udf.FunctionConfig{
				Socket:  filepath.Join(tdir, "mirror.py.sock"),
				Timeout: toml.Duration(time.Minute),
			},
		},
	}
	for _, agent := range agents {
		cmd := agent.startFunc()
		cmd.Start()
		defer cmd.Process.Signal(os.Interrupt)
		if err != nil {
			t.Fatal(err)
		}
		c := NewConfig()
		c.UDF.Functions = map[string]udf.FunctionConfig{
			"mirror": agent.config,
		}
		testStreamAgentSocket(t, c)
	}
}

func testStreamAgentSocket(t *testing.T, c *server.Config) {
	s := NewServer(c)
	err := s.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cli := Client(s)

	id := "testUDFTask"
	ttype := client.StreamTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `stream
    |from()
        .measurement('test')
        .groupBy('group')
    @mirror()
    |window()
        .period(10s)
        .every(10s)
    |count('value')
    |httpOut('count')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), id)

	// Request data before any writes and expect null responses
	nullResponse := `{}`
	err = s.HTTPGetRetry(endpoint, nullResponse, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}

	points := `test,group=a value=1 0000000000
test,group=a value=1 0000000001
test,group=a value=1 0000000002
test,group=a value=1 0000000003
test,group=a value=1 0000000004
test,group=a value=1 0000000005
test,group=a value=1 0000000006
test,group=a value=1 0000000007
test,group=a value=1 0000000008
test,group=a value=1 0000000009
test,group=a value=0 0000000010
test,group=a value=0 0000000011
`
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", points, v)

	exp := `{"series":[{"name":"test","tags":{"group":"a"},"columns":["time","count"],"values":[["1970-01-01T00:00:10Z",10]]}]}`
	err = s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*5)
	if err != nil {
		t.Error(err)
	}
}

// If this test fails due to missing python dependencies, run 'INSTALL_PREFIX=/usr/local ./install-deps.sh' from the root directory of the
// kapacitor project.
func TestServer_UDFBatchAgents(t *testing.T) {
	tdir, err := ioutil.TempDir("", "kapacitor_server_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tdir)

	agents := []struct {
		buildFunc func() error
		config    udf.FunctionConfig
	}{
		// Go
		{
			buildFunc: func() error {
				// Explicitly compile the binary.
				// We could just use 'go run' but I ran into race conditions
				// where 'go run' was not handing off to the compiled process in time
				// and I didn't care to dig into 'go run's specific behavior.
				cmd := exec.Command(
					"go",
					"build",
					"-o",
					filepath.Join(tdir, "outliers"),
					filepath.Join(udfDir, "agent/examples/outliers/outliers.go"),
				)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Log(string(out))
					return err
				}
				return nil
			},
			config: udf.FunctionConfig{
				Prog:    filepath.Join(tdir, "outliers"),
				Timeout: toml.Duration(time.Minute),
			},
		},
		// Python
		{
			buildFunc: func() error { return nil },
			config: udf.FunctionConfig{
				Prog:    "python2",
				Args:    []string{"-u", filepath.Join(udfDir, "agent/examples/outliers/outliers.py")},
				Timeout: toml.Duration(time.Minute),
				Env: map[string]string{
					"PYTHONPATH": strings.Join(
						[]string{filepath.Join(udfDir, "agent/py"), os.Getenv("PYTHONPATH")},
						string(filepath.ListSeparator),
					),
				},
			},
		},
	}
	for _, agent := range agents {
		err := agent.buildFunc()
		if err != nil {
			t.Fatal(err)
		}
		c := NewConfig()
		c.UDF.Functions = map[string]udf.FunctionConfig{
			"outliers": agent.config,
		}
		testBatchAgent(t, c)
	}
}

func testBatchAgent(t *testing.T, c *server.Config) {
	count := 0
	stopTimeC := make(chan time.Time, 2)
	db := NewInfluxDB(func(q string) *iclient.Response {
		stmt, err := influxql.ParseStatement(q)
		if err != nil {
			return &iclient.Response{Err: err.Error()}
		}
		slct, ok := stmt.(*influxql.SelectStatement)
		if !ok {
			return nil
		}
		cond, ok := slct.Condition.(*influxql.BinaryExpr)
		if !ok {
			return &iclient.Response{Err: "expected select condition to be binary expression"}
		}
		stopTimeExpr, ok := cond.RHS.(*influxql.BinaryExpr)
		if !ok {
			return &iclient.Response{Err: "expected select condition rhs to be binary expression"}
		}
		stopTL, ok := stopTimeExpr.RHS.(*influxql.StringLiteral)
		if !ok {
			return &iclient.Response{Err: "expected select condition rhs to be string literal"}
		}
		count++
		switch count {
		case 1, 2:
			stopTime, err := time.Parse(time.RFC3339Nano, stopTL.Val)
			if err != nil {
				return &iclient.Response{Err: err.Error()}
			}
			stopTimeC <- stopTime
			data := []float64{
				5,
				6,
				7,
				13,
				33,
				35,
				36,
				45,
				46,
				47,
				48,
				50,
				51,
				52,
				53,
				54,
				80,
				85,
				90,
				100,
			}
			// Shuffle data using count as seed.
			// Data order should not effect the result.
			r := rand.New(rand.NewSource(int64(count)))
			for i := range data {
				j := r.Intn(i + 1)
				data[i], data[j] = data[j], data[i]
			}

			// Create set values with time from shuffled data.
			values := make([][]interface{}, len(data))
			for i, value := range data {
				values[i] = []interface{}{
					stopTime.Add(time.Duration(i-len(data)) * time.Millisecond).Format(time.RFC3339Nano),
					value,
				}
			}

			return &iclient.Response{
				Results: []iclient.Result{{
					Series: []models.Row{{
						Name:    "cpu",
						Columns: []string{"time", "value"},
						Tags: map[string]string{
							"count": strconv.FormatInt(int64(count%2), 10),
						},
						Values: values,
					}},
				}},
			}
		default:
			return nil
		}
	})
	c.InfluxDB[0].URLs = []string{db.URL()}
	c.InfluxDB[0].Enabled = true
	s := NewServer(c)
	err := s.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cli := Client(s)

	id := "testUDFTask"
	ttype := client.BatchTask
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	tick := `batch
    |query(' SELECT value from mydb.myrp.cpu ')
        .period(5ms)
        .every(5ms)
        .groupBy('count')
    @outliers()
        .field('value')
        .scale(1.5)
    |count('value')
    |httpOut('count')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	stopTimes := make([]time.Time, 2)
	for i := range stopTimes {
		timeout := time.NewTicker(100 * time.Millisecond)
		defer timeout.Stop()
		select {
		case <-timeout.C:
			t.Fatal("timedout waiting for query")
		case stopTime := <-stopTimeC:
			stopTimes[i] = stopTime
		}
	}
	endpoint := fmt.Sprintf("%s/tasks/%s/count", s.URL(), id)
	exp := fmt.Sprintf(
		`{"series":[{"name":"cpu","tags":{"count":"1"},"columns":["time","count"],"values":[["%s",5]]},{"name":"cpu","tags":{"count":"0"},"columns":["time","count"],"values":[["%s",5]]}]}`,
		stopTimes[0].Format(time.RFC3339Nano),
		stopTimes[1].Format(time.RFC3339Nano),
	)
	err = s.HTTPGetRetry(endpoint, exp, 100, time.Millisecond*50)
	if err != nil {
		t.Error(err)
	}
	_, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServer_CreateTask_Defaults(t *testing.T) {
	s, cli := OpenDefaultServer()
	baseURL := s.URL()

	body := `
{
    "id" : "TASK_ID",
    "type" : "stream",
    "dbrps": [{"db": "DATABASE_NAME", "rp" : "RP_NAME"}],
    "script": "stream\n    |from()\n        .measurement('cpu')\n"
}`
	resp, err := http.Post(baseURL+"/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if exp, got := http.StatusOK, resp.StatusCode; exp != got {
		t.Errorf("unexpected status code, got %d exp %d", got, exp)
	}

	id := "TASK_ID"
	tick := "stream\n    |from()\n        .measurement('cpu')\n"
	dbrps := []client.DBRP{
		{
			Database:        "DATABASE_NAME",
			RetentionPolicy: "RP_NAME",
		},
	}
	ti, err := cli.Task(cli.TaskLink(id), nil)
	if err != nil {
		t.Fatal(err)
	}

	if ti.Error != "" {
		t.Fatal(ti.Error)
	}
	if ti.ID != id {
		t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
	}
	if ti.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
	}
	if ti.Status != client.Disabled {
		t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Disabled)
	}
	if !reflect.DeepEqual(ti.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
	}
	if ti.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
	}
	dot := "digraph TASK_ID {\nstream0 -> from1;\n}"
	if ti.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
	}
}

func TestServer_ListTask_Defaults(t *testing.T) {
	s, cli := OpenDefaultServer()
	baseURL := s.URL()
	dbrps := []client.DBRP{{
		Database:        "mydb",
		RetentionPolicy: "myrp",
	}}
	id := "task_id"
	tick := "stream\n    |from()\n"
	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         id,
		Type:       client.StreamTask,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(baseURL + "/tasks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if exp, got := http.StatusOK, resp.StatusCode; exp != got {
		t.Errorf("unexpected status code, got %d exp %d", got, exp)
	}
	// Response type
	type response struct {
		Tasks []client.Task `json:"tasks"`
	}
	dec := json.NewDecoder(resp.Body)
	tasks := response{}
	dec.Decode(&tasks)
	if exp, got := 1, len(tasks.Tasks); exp != got {
		t.Fatalf("unexpected tasks count, got %d exp %d", got, exp)
	}

	task = tasks.Tasks[0]
	if task.ID != id {
		t.Fatalf("unexpected id got %s exp %s", task.ID, id)
	}
	if task.Type != client.StreamTask {
		t.Fatalf("unexpected type got %v exp %v", task.Type, client.StreamTask)
	}
	if task.Status != client.Disabled {
		t.Fatalf("unexpected status got %v exp %v", task.Status, client.Disabled)
	}
	if !reflect.DeepEqual(task.DBRPs, dbrps) {
		t.Fatalf("unexpected dbrps got %s exp %s", task.DBRPs, dbrps)
	}
	if task.TICKscript != tick {
		t.Fatalf("unexpected TICKscript got %s exp %s", task.TICKscript, tick)
	}
	dot := "digraph task_id {\nstream0 -> from1;\n}"
	if task.Dot != dot {
		t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", task.Dot, dot)
	}
}

func TestServer_CreateTask_ValidIDs(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()

	testCases := []struct {
		id    string
		valid bool
	}{
		{
			id:    "task_id",
			valid: true,
		},
		{
			id:    "task_id7",
			valid: true,
		},
		{
			id:    "task.id7",
			valid: true,
		},
		{
			id:    "task-id7",
			valid: true,
		},
		{
			id:    "tásk7",
			valid: true,
		},
		{
			id:    "invalid id",
			valid: false,
		},
		{
			id:    "invalid*id",
			valid: false,
		},
		{
			id:    "task/id7",
			valid: false,
		},
	}

	for _, tc := range testCases {
		id := tc.id
		ttype := client.StreamTask
		dbrps := []client.DBRP{
			{
				Database:        "mydb",
				RetentionPolicy: "myrp",
			},
		}
		tick := `stream
    |from()
        .measurement('test')
`
		task, err := cli.CreateTask(client.CreateTaskOptions{
			ID:         id,
			Type:       ttype,
			DBRPs:      dbrps,
			TICKscript: tick,
			Status:     client.Disabled,
		})
		if !tc.valid {
			exp := fmt.Sprintf("task ID must contain only letters, numbers, '-', '.' and '_'. %q", id)
			if err.Error() != exp {
				t.Errorf("unexpected error: got %s exp %s", err.Error(), exp)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}

		ti, err := cli.Task(task.Link, nil)
		if err != nil {
			t.Fatal(err)
		}

		if ti.Error != "" {
			t.Fatal(ti.Error)
		}
		if ti.ID != id {
			t.Fatalf("unexpected id got %s exp %s", ti.ID, id)
		}
		if ti.Type != client.StreamTask {
			t.Fatalf("unexpected type got %v exp %v", ti.Type, client.StreamTask)
		}
		if ti.Status != client.Disabled {
			t.Fatalf("unexpected status got %v exp %v", ti.Status, client.Disabled)
		}
		if !reflect.DeepEqual(ti.DBRPs, dbrps) {
			t.Fatalf("unexpected dbrps got %s exp %s", ti.DBRPs, dbrps)
		}
		if ti.TICKscript != tick {
			t.Fatalf("unexpected TICKscript got %s exp %s", ti.TICKscript, tick)
		}
		dot := "digraph " + id + " {\nstream0 -> from1;\n}"
		if ti.Dot != dot {
			t.Fatalf("unexpected dot\ngot\n%s\nexp\n%s\n", ti.Dot, dot)
		}
	}
}

func TestServer_CreateRecording_ValidIDs(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`
	_, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         "task_id",
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		id    string
		valid bool
	}{
		{
			id:    "recording_id",
			valid: true,
		},
		{
			id:    "recording_id7",
			valid: true,
		},
		{
			id:    "recording.id7",
			valid: true,
		},
		{
			id:    "recording-id7",
			valid: true,
		},
		{
			id:    "récording7",
			valid: true,
		},
		{
			id:    "invalid id",
			valid: false,
		},
		{
			id:    "invalid*id",
			valid: false,
		},
		{
			id:    "recording/id7",
			valid: false,
		},
	}

	for _, tc := range testCases {
		id := tc.id
		recording, err := cli.RecordStream(client.RecordStreamOptions{
			ID:   id,
			Task: "task_id",
			Stop: time.Date(1970, 1, 1, 0, 0, 10, 0, time.UTC),
		})
		if !tc.valid {
			exp := fmt.Sprintf("recording ID must contain only letters, numbers, '-', '.' and '_'. %q", id)
			if err.Error() != exp {
				t.Errorf("unexpected error: got %s exp %s", err.Error(), exp)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}

		recording, err = cli.Recording(recording.Link)
		if err != nil {
			t.Fatal(err)
		}

		if exp, got := id, recording.ID; got != exp {
			t.Errorf("unexpected recording ID got %s exp %s", got, exp)
		}
	}
}

func TestServer_CreateReplay_ValidIDs(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()
	ttype := client.StreamTask
	dbrps := []client.DBRP{
		{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		},
	}
	tick := `stream
    |from()
        .measurement('test')
`

	_, err := cli.CreateTask(client.CreateTaskOptions{
		ID:         "task_id",
		Type:       ttype,
		DBRPs:      dbrps,
		TICKscript: tick,
		Status:     client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cli.RecordStream(client.RecordStreamOptions{
		ID:   "recording_id",
		Task: "task_id",
		Stop: time.Date(1970, 1, 1, 0, 0, 10, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		id    string
		valid bool
	}{
		{
			id:    "replay_id",
			valid: true,
		},
		{
			id:    "replay_id7",
			valid: true,
		},
		{
			id:    "replay.id7",
			valid: true,
		},
		{
			id:    "replay-id7",
			valid: true,
		},
		{
			id:    "réplay7",
			valid: true,
		},
		{
			id:    "invalid id",
			valid: false,
		},
		{
			id:    "invalid*id",
			valid: false,
		},
		{
			id:    "replay/id7",
			valid: false,
		},
	}

	for _, tc := range testCases {
		id := tc.id
		replay, err := cli.CreateReplay(client.CreateReplayOptions{
			ID:            id,
			Task:          "task_id",
			Recording:     "recording_id",
			Clock:         client.Fast,
			RecordingTime: true,
		})
		if !tc.valid {
			exp := fmt.Sprintf("replay ID must contain only letters, numbers, '-', '.' and '_'. %q", id)
			if err.Error() != exp {
				t.Errorf("unexpected error: got %s exp %s", err.Error(), exp)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}

		replay, err = cli.Replay(replay.Link)
		if err != nil {
			t.Fatal(err)
		}

		if exp, got := id, replay.ID; got != exp {
			t.Errorf("unexpected replay ID got %s exp %s", got, exp)
		}
	}
}

func TestServer_UpdateConfig(t *testing.T) {
	type updateAction struct {
		element      string
		updateAction client.ConfigUpdateAction
		expSection   client.ConfigSection
		expElement   client.ConfigElement
	}
	db := NewInfluxDB(func(q string) *iclient.Response {
		return &iclient.Response{}
	})
	testCases := []struct {
		section           string
		element           string
		setDefaults       func(*server.Config)
		expDefaultSection client.ConfigSection
		expDefaultElement client.ConfigElement
		updates           []updateAction
	}{
		{
			section: "influxdb",
			element: "default",
			setDefaults: func(c *server.Config) {
				c.InfluxDB[0].Enabled = true
				c.InfluxDB[0].Username = "bob"
				c.InfluxDB[0].Password = "secret"
				c.InfluxDB[0].URLs = []string{db.URL()}
				// Set really long timeout since we shouldn't hit it
				c.InfluxDB[0].StartUpTimeout = toml.Duration(time.Hour)
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
					Options: map[string]interface{}{
						"default":                     false,
						"disable-subscriptions":       false,
						"enabled":                     true,
						"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
						"http-port":                   float64(0),
						"insecure-skip-verify":        false,
						"kapacitor-hostname":          "",
						"name":                        "default",
						"password":                    true,
						"ssl-ca":                      "",
						"ssl-cert":                    "",
						"ssl-key":                     "",
						"startup-timeout":             "1h0m0s",
						"subscription-protocol":       "http",
						"subscriptions":               nil,
						"subscriptions-sync-interval": "1m0s",
						"timeout":                     "0s",
						"udp-bind":                    "",
						"udp-buffer":                  float64(1e3),
						"udp-read-buffer":             float64(0),
						"urls":                        []interface{}{db.URL()},
						"username":                    "bob",
					},
					Redacted: []string{
						"password",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
				Options: map[string]interface{}{
					"default":                     false,
					"disable-subscriptions":       false,
					"enabled":                     true,
					"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
					"http-port":                   float64(0),
					"insecure-skip-verify":        false,
					"kapacitor-hostname":          "",
					"name":                        "default",
					"password":                    true,
					"ssl-ca":                      "",
					"ssl-cert":                    "",
					"ssl-key":                     "",
					"startup-timeout":             "1h0m0s",
					"subscription-protocol":       "http",
					"subscriptions":               nil,
					"subscriptions-sync-interval": "1m0s",
					"timeout":                     "0s",
					"udp-bind":                    "",
					"udp-buffer":                  float64(1e3),
					"udp-read-buffer":             float64(0),
					"urls":                        []interface{}{db.URL()},
					"username":                    "bob",
				},
				Redacted: []string{
					"password",
				},
			},
			updates: []updateAction{
				{
					// Set Invalid URL to make sure we can fix it without waiting for connection timeouts
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"urls": []string{"http://192.0.2.0:8086"},
						},
					},
					element: "default",
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
							Options: map[string]interface{}{
								"default":                     false,
								"disable-subscriptions":       false,
								"enabled":                     true,
								"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
								"http-port":                   float64(0),
								"insecure-skip-verify":        false,
								"kapacitor-hostname":          "",
								"name":                        "default",
								"password":                    true,
								"ssl-ca":                      "",
								"ssl-cert":                    "",
								"ssl-key":                     "",
								"startup-timeout":             "1h0m0s",
								"subscription-protocol":       "http",
								"subscriptions":               nil,
								"subscriptions-sync-interval": "1m0s",
								"timeout":                     "0s",
								"udp-bind":                    "",
								"udp-buffer":                  float64(1e3),
								"udp-read-buffer":             float64(0),
								"urls":                        []interface{}{"http://192.0.2.0:8086"},
								"username":                    "bob",
							},
							Redacted: []string{
								"password",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
						Options: map[string]interface{}{
							"default":                     false,
							"disable-subscriptions":       false,
							"enabled":                     true,
							"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
							"http-port":                   float64(0),
							"insecure-skip-verify":        false,
							"kapacitor-hostname":          "",
							"name":                        "default",
							"password":                    true,
							"ssl-ca":                      "",
							"ssl-cert":                    "",
							"ssl-key":                     "",
							"startup-timeout":             "1h0m0s",
							"subscription-protocol":       "http",
							"subscriptions":               nil,
							"subscriptions-sync-interval": "1m0s",
							"timeout":                     "0s",
							"udp-bind":                    "",
							"udp-buffer":                  float64(1e3),
							"udp-read-buffer":             float64(0),
							"urls":                        []interface{}{"http://192.0.2.0:8086"},
							"username":                    "bob",
						},
						Redacted: []string{
							"password",
						},
					},
				},
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"default":               true,
							"subscription-protocol": "https",
							"subscriptions":         map[string][]string{"_internal": []string{"monitor"}},
						},
					},
					element: "default",
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
							Options: map[string]interface{}{
								"default":                     true,
								"disable-subscriptions":       false,
								"enabled":                     true,
								"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
								"http-port":                   float64(0),
								"insecure-skip-verify":        false,
								"kapacitor-hostname":          "",
								"name":                        "default",
								"password":                    true,
								"ssl-ca":                      "",
								"ssl-cert":                    "",
								"ssl-key":                     "",
								"startup-timeout":             "1h0m0s",
								"subscription-protocol":       "https",
								"subscriptions":               map[string]interface{}{"_internal": []interface{}{"monitor"}},
								"subscriptions-sync-interval": "1m0s",
								"timeout":                     "0s",
								"udp-bind":                    "",
								"udp-buffer":                  float64(1e3),
								"udp-read-buffer":             float64(0),
								"urls":                        []interface{}{"http://192.0.2.0:8086"},
								"username":                    "bob",
							},
							Redacted: []string{
								"password",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
						Options: map[string]interface{}{
							"default":                     true,
							"disable-subscriptions":       false,
							"enabled":                     true,
							"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
							"http-port":                   float64(0),
							"insecure-skip-verify":        false,
							"kapacitor-hostname":          "",
							"name":                        "default",
							"password":                    true,
							"ssl-ca":                      "",
							"ssl-cert":                    "",
							"ssl-key":                     "",
							"startup-timeout":             "1h0m0s",
							"subscription-protocol":       "https",
							"subscriptions":               map[string]interface{}{"_internal": []interface{}{"monitor"}},
							"subscriptions-sync-interval": "1m0s",
							"timeout":                     "0s",
							"udp-bind":                    "",
							"udp-buffer":                  float64(1e3),
							"udp-read-buffer":             float64(0),
							"urls":                        []interface{}{"http://192.0.2.0:8086"},
							"username":                    "bob",
						},
						Redacted: []string{
							"password",
						},
					},
				},
				{
					updateAction: client.ConfigUpdateAction{
						Delete: []string{"urls"},
					},
					element: "default",
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
							Options: map[string]interface{}{
								"default":                     true,
								"disable-subscriptions":       false,
								"enabled":                     true,
								"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
								"http-port":                   float64(0),
								"insecure-skip-verify":        false,
								"kapacitor-hostname":          "",
								"name":                        "default",
								"password":                    true,
								"ssl-ca":                      "",
								"ssl-cert":                    "",
								"ssl-key":                     "",
								"startup-timeout":             "1h0m0s",
								"subscription-protocol":       "https",
								"subscriptions":               map[string]interface{}{"_internal": []interface{}{"monitor"}},
								"subscriptions-sync-interval": "1m0s",
								"timeout":                     "0s",
								"udp-bind":                    "",
								"udp-buffer":                  float64(1e3),
								"udp-read-buffer":             float64(0),
								"urls":                        []interface{}{db.URL()},
								"username":                    "bob",
							},
							Redacted: []string{
								"password",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
						Options: map[string]interface{}{
							"default":                     true,
							"disable-subscriptions":       false,
							"enabled":                     true,
							"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
							"http-port":                   float64(0),
							"insecure-skip-verify":        false,
							"kapacitor-hostname":          "",
							"name":                        "default",
							"password":                    true,
							"ssl-ca":                      "",
							"ssl-cert":                    "",
							"ssl-key":                     "",
							"startup-timeout":             "1h0m0s",
							"subscription-protocol":       "https",
							"subscriptions":               map[string]interface{}{"_internal": []interface{}{"monitor"}},
							"subscriptions-sync-interval": "1m0s",
							"timeout":                     "0s",
							"udp-bind":                    "",
							"udp-buffer":                  float64(1e3),
							"udp-read-buffer":             float64(0),
							"urls":                        []interface{}{db.URL()},
							"username":                    "bob",
						},
						Redacted: []string{
							"password",
						},
					},
				},
				{
					updateAction: client.ConfigUpdateAction{
						Add: map[string]interface{}{
							"name": "new",
							"urls": []string{db.URL()},
						},
					},
					element: "new",
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb"},
						Elements: []client.ConfigElement{
							{
								Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/default"},
								Options: map[string]interface{}{
									"default":                     true,
									"disable-subscriptions":       false,
									"enabled":                     true,
									"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
									"http-port":                   float64(0),
									"insecure-skip-verify":        false,
									"kapacitor-hostname":          "",
									"name":                        "default",
									"password":                    true,
									"ssl-ca":                      "",
									"ssl-cert":                    "",
									"ssl-key":                     "",
									"startup-timeout":             "1h0m0s",
									"subscription-protocol":       "https",
									"subscriptions":               map[string]interface{}{"_internal": []interface{}{"monitor"}},
									"subscriptions-sync-interval": "1m0s",
									"timeout":                     "0s",
									"udp-bind":                    "",
									"udp-buffer":                  float64(1e3),
									"udp-read-buffer":             float64(0),
									"urls":                        []interface{}{db.URL()},
									"username":                    "bob",
								},
								Redacted: []string{
									"password",
								},
							},
							{
								Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/new"},
								Options: map[string]interface{}{
									"default":                     false,
									"disable-subscriptions":       false,
									"enabled":                     false,
									"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
									"http-port":                   float64(0),
									"insecure-skip-verify":        false,
									"kapacitor-hostname":          "",
									"name":                        "new",
									"password":                    false,
									"ssl-ca":                      "",
									"ssl-cert":                    "",
									"ssl-key":                     "",
									"startup-timeout":             "5m0s",
									"subscription-protocol":       "http",
									"subscriptions":               nil,
									"subscriptions-sync-interval": "1m0s",
									"timeout":                     "0s",
									"udp-bind":                    "",
									"udp-buffer":                  float64(1e3),
									"udp-read-buffer":             float64(0),
									"urls":                        []interface{}{db.URL()},
									"username":                    "",
								},
								Redacted: []string{
									"password",
								},
							},
						},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/influxdb/new"},
						Options: map[string]interface{}{
							"default":                     false,
							"disable-subscriptions":       false,
							"enabled":                     false,
							"excluded-subscriptions":      map[string]interface{}{"_kapacitor": []interface{}{"autogen"}},
							"http-port":                   float64(0),
							"insecure-skip-verify":        false,
							"kapacitor-hostname":          "",
							"name":                        "new",
							"password":                    false,
							"ssl-ca":                      "",
							"ssl-cert":                    "",
							"ssl-key":                     "",
							"startup-timeout":             "5m0s",
							"subscription-protocol":       "http",
							"subscriptions":               nil,
							"subscriptions-sync-interval": "1m0s",
							"timeout":                     "0s",
							"udp-bind":                    "",
							"udp-buffer":                  float64(1e3),
							"udp-read-buffer":             float64(0),
							"urls":                        []interface{}{db.URL()},
							"username":                    "",
						},
						Redacted: []string{
							"password",
						},
					},
				},
			},
		},
		{
			section: "alerta",
			setDefaults: func(c *server.Config) {
				c.Alerta.URL = "http://alerta.example.com"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/alerta"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/alerta/"},
					Options: map[string]interface{}{
						"enabled":     false,
						"environment": "",
						"origin":      "",
						"token":       false,
						"url":         "http://alerta.example.com",
						"insecure-skip-verify": false,
					},
					Redacted: []string{
						"token",
					}},
				},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/alerta/"},
				Options: map[string]interface{}{
					"enabled":     false,
					"environment": "",
					"origin":      "",
					"token":       false,
					"url":         "http://alerta.example.com",
					"insecure-skip-verify": false,
				},
				Redacted: []string{
					"token",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"token":  "token",
							"origin": "kapacitor",
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/alerta"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/alerta/"},
							Options: map[string]interface{}{
								"enabled":     false,
								"environment": "",
								"origin":      "kapacitor",
								"token":       true,
								"url":         "http://alerta.example.com",
								"insecure-skip-verify": false,
							},
							Redacted: []string{
								"token",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/alerta/"},
						Options: map[string]interface{}{
							"enabled":     false,
							"environment": "",
							"origin":      "kapacitor",
							"token":       true,
							"url":         "http://alerta.example.com",
							"insecure-skip-verify": false,
						},
						Redacted: []string{
							"token",
						},
					},
				},
			},
		},
		{
			section: "kubernetes",
			setDefaults: func(c *server.Config) {
				c.Kubernetes.APIServers = []string{"http://localhost:80001"}
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/kubernetes"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/kubernetes/"},
					Options: map[string]interface{}{
						"api-servers": []interface{}{"http://localhost:80001"},
						"ca-path":     "",
						"enabled":     false,
						"in-cluster":  false,
						"namespace":   "",
						"token":       false,
					},
					Redacted: []string{
						"token",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/kubernetes/"},
				Options: map[string]interface{}{
					"api-servers": []interface{}{"http://localhost:80001"},
					"ca-path":     "",
					"enabled":     false,
					"in-cluster":  false,
					"namespace":   "",
					"token":       false,
				},
				Redacted: []string{
					"token",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"token": "secret",
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/kubernetes"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/kubernetes/"},
							Options: map[string]interface{}{
								"api-servers": []interface{}{"http://localhost:80001"},
								"ca-path":     "",
								"enabled":     false,
								"in-cluster":  false,
								"namespace":   "",
								"token":       true,
							},
							Redacted: []string{
								"token",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/kubernetes/"},
						Options: map[string]interface{}{
							"api-servers": []interface{}{"http://localhost:80001"},
							"ca-path":     "",
							"enabled":     false,
							"in-cluster":  false,
							"namespace":   "",
							"token":       true,
						},
						Redacted: []string{
							"token",
						},
					},
				},
			},
		},
		{
			section: "hipchat",
			setDefaults: func(c *server.Config) {
				c.HipChat.URL = "http://hipchat.example.com"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/hipchat"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/hipchat/"},
					Options: map[string]interface{}{
						"enabled":            false,
						"global":             false,
						"room":               "",
						"state-changes-only": false,
						"token":              false,
						"url":                "http://hipchat.example.com",
					},
					Redacted: []string{
						"token",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/hipchat/"},
				Options: map[string]interface{}{
					"enabled":            false,
					"global":             false,
					"room":               "",
					"state-changes-only": false,
					"token":              false,
					"url":                "http://hipchat.example.com",
				},
				Redacted: []string{
					"token",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"token": "token",
							"room":  "kapacitor",
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/hipchat"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/hipchat/"},
							Options: map[string]interface{}{
								"enabled":            false,
								"global":             false,
								"room":               "kapacitor",
								"state-changes-only": false,
								"token":              true,
								"url":                "http://hipchat.example.com",
							},
							Redacted: []string{
								"token",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/hipchat/"},
						Options: map[string]interface{}{
							"enabled":            false,
							"global":             false,
							"room":               "kapacitor",
							"state-changes-only": false,
							"token":              true,
							"url":                "http://hipchat.example.com",
						},
						Redacted: []string{
							"token",
						},
					},
				},
			},
		},
		{
			section: "opsgenie",
			setDefaults: func(c *server.Config) {
				c.OpsGenie.URL = "http://opsgenie.example.com"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/opsgenie"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/opsgenie/"},
					Options: map[string]interface{}{
						"api-key":      false,
						"enabled":      false,
						"global":       false,
						"recipients":   nil,
						"recovery_url": opsgenie.DefaultOpsGenieRecoveryURL,
						"teams":        nil,
						"url":          "http://opsgenie.example.com",
					},
					Redacted: []string{
						"api-key",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/opsgenie/"},
				Options: map[string]interface{}{
					"api-key":      false,
					"enabled":      false,
					"global":       false,
					"recipients":   nil,
					"recovery_url": opsgenie.DefaultOpsGenieRecoveryURL,
					"teams":        nil,
					"url":          "http://opsgenie.example.com",
				},
				Redacted: []string{
					"api-key",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"api-key": "token",
							"global":  true,
							"teams":   []string{"teamA", "teamB"},
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/opsgenie"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/opsgenie/"},
							Options: map[string]interface{}{
								"api-key":      true,
								"enabled":      false,
								"global":       true,
								"recipients":   nil,
								"recovery_url": opsgenie.DefaultOpsGenieRecoveryURL,
								"teams":        []interface{}{"teamA", "teamB"},
								"url":          "http://opsgenie.example.com",
							},
							Redacted: []string{
								"api-key",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/opsgenie/"},
						Options: map[string]interface{}{
							"api-key":      true,
							"enabled":      false,
							"global":       true,
							"recipients":   nil,
							"recovery_url": opsgenie.DefaultOpsGenieRecoveryURL,
							"teams":        []interface{}{"teamA", "teamB"},
							"url":          "http://opsgenie.example.com",
						},
						Redacted: []string{
							"api-key",
						},
					},
				},
			},
		},
		{
			section: "pagerduty",
			setDefaults: func(c *server.Config) {
				c.PagerDuty.ServiceKey = "secret"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/pagerduty"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/pagerduty/"},
					Options: map[string]interface{}{
						"enabled":     false,
						"global":      false,
						"service-key": true,
						"url":         pagerduty.DefaultPagerDutyAPIURL,
					},
					Redacted: []string{
						"service-key",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/pagerduty/"},
				Options: map[string]interface{}{
					"enabled":     false,
					"global":      false,
					"service-key": true,
					"url":         pagerduty.DefaultPagerDutyAPIURL,
				},
				Redacted: []string{
					"service-key",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"service-key": "",
							"enabled":     true,
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/pagerduty"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/pagerduty/"},
							Options: map[string]interface{}{
								"enabled":     true,
								"global":      false,
								"service-key": false,
								"url":         pagerduty.DefaultPagerDutyAPIURL,
							},
							Redacted: []string{
								"service-key",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/pagerduty/"},
						Options: map[string]interface{}{
							"enabled":     true,
							"global":      false,
							"service-key": false,
							"url":         pagerduty.DefaultPagerDutyAPIURL,
						},
						Redacted: []string{
							"service-key",
						},
					},
				},
			},
		},
		{
			section: "smtp",
			setDefaults: func(c *server.Config) {
				c.SMTP.Host = "smtp.example.com"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/smtp"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/smtp/"},
					Options: map[string]interface{}{
						"enabled":            false,
						"from":               "",
						"global":             false,
						"host":               "smtp.example.com",
						"idle-timeout":       "30s",
						"no-verify":          false,
						"password":           false,
						"port":               float64(25),
						"state-changes-only": false,
						"to":                 nil,
						"username":           "",
					},
					Redacted: []string{
						"password",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/smtp/"},
				Options: map[string]interface{}{
					"enabled":            false,
					"from":               "",
					"global":             false,
					"host":               "smtp.example.com",
					"idle-timeout":       "30s",
					"no-verify":          false,
					"password":           false,
					"port":               float64(25),
					"state-changes-only": false,
					"to":                 nil,
					"username":           "",
				},
				Redacted: []string{
					"password",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"idle-timeout": "1m0s",
							"global":       true,
							"password":     "secret",
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/smtp"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/smtp/"},
							Options: map[string]interface{}{
								"enabled":            false,
								"from":               "",
								"global":             true,
								"host":               "smtp.example.com",
								"idle-timeout":       "1m0s",
								"no-verify":          false,
								"password":           true,
								"port":               float64(25),
								"state-changes-only": false,
								"to":                 nil,
								"username":           "",
							},
							Redacted: []string{
								"password",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/smtp/"},
						Options: map[string]interface{}{
							"enabled":            false,
							"from":               "",
							"global":             true,
							"host":               "smtp.example.com",
							"idle-timeout":       "1m0s",
							"no-verify":          false,
							"password":           true,
							"port":               float64(25),
							"state-changes-only": false,
							"to":                 nil,
							"username":           "",
						},
						Redacted: []string{
							"password",
						},
					},
				},
			},
		},
		{
			section: "sensu",
			setDefaults: func(c *server.Config) {
				c.Sensu.Addr = "sensu.example.com:3000"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/sensu"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/sensu/"},
					Options: map[string]interface{}{
						"addr":    "sensu.example.com:3000",
						"enabled": false,
						"source":  "Kapacitor",
					},
					Redacted: nil,
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/sensu/"},
				Options: map[string]interface{}{
					"addr":    "sensu.example.com:3000",
					"enabled": false,
					"source":  "Kapacitor",
				},
				Redacted: nil,
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"addr":    "sensu.local:3000",
							"enabled": true,
							"source":  "",
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/sensu"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/sensu/"},
							Options: map[string]interface{}{
								"addr":    "sensu.local:3000",
								"enabled": true,
								"source":  "",
							},
							Redacted: nil,
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/sensu/"},
						Options: map[string]interface{}{
							"addr":    "sensu.local:3000",
							"enabled": true,
							"source":  "",
						},
						Redacted: nil,
					},
				},
			},
		},
		{
			section: "slack",
			setDefaults: func(c *server.Config) {
				c.Slack.Global = true
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/slack"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/slack/"},
					Options: map[string]interface{}{
						"channel":            "",
						"enabled":            false,
						"global":             true,
						"icon-emoji":         "",
						"state-changes-only": false,
						"url":                false,
						"username":           "kapacitor",
					},
					Redacted: []string{
						"url",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/slack/"},
				Options: map[string]interface{}{
					"channel":            "",
					"enabled":            false,
					"global":             true,
					"icon-emoji":         "",
					"state-changes-only": false,
					"url":                false,
					"username":           "kapacitor",
				},
				Redacted: []string{
					"url",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"enabled": true,
							"global":  false,
							"channel": "#general",
							"url":     "http://slack.example.com/secret-token",
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/slack"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/slack/"},
							Options: map[string]interface{}{
								"channel":            "#general",
								"enabled":            true,
								"global":             false,
								"icon-emoji":         "",
								"state-changes-only": false,
								"url":                true,
								"username":           "kapacitor",
							},
							Redacted: []string{
								"url",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/slack/"},
						Options: map[string]interface{}{
							"channel":            "#general",
							"enabled":            true,
							"global":             false,
							"icon-emoji":         "",
							"state-changes-only": false,
							"url":                true,
							"username":           "kapacitor",
						},
						Redacted: []string{
							"url",
						},
					},
				},
			},
		},
		{
			section: "snmptrap",
			setDefaults: func(c *server.Config) {
				c.SNMPTrap.Community = "test"
				c.SNMPTrap.Retries = 2.0
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/snmptrap"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/snmptrap/"},
					Options: map[string]interface{}{
						"addr":      "localhost:162",
						"enabled":   false,
						"community": true,
						"retries":   2.0,
					},
					Redacted: []string{
						"community",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/snmptrap/"},
				Options: map[string]interface{}{
					"addr":      "localhost:162",
					"enabled":   false,
					"community": true,
					"retries":   2.0,
				},
				Redacted: []string{
					"community",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"enabled":   true,
							"addr":      "snmptrap.example.com:162",
							"community": "public",
							"retries":   1.0,
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/snmptrap"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/snmptrap/"},
							Options: map[string]interface{}{
								"addr":      "snmptrap.example.com:162",
								"enabled":   true,
								"community": true,
								"retries":   1.0,
							},
							Redacted: []string{
								"community",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/snmptrap/"},
						Options: map[string]interface{}{
							"addr":      "snmptrap.example.com:162",
							"enabled":   true,
							"community": true,
							"retries":   1.0,
						},
						Redacted: []string{
							"community",
						},
					},
				},
			},
		},
		{
			section: "talk",
			setDefaults: func(c *server.Config) {
				c.Talk.AuthorName = "Kapacitor"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/talk"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/talk/"},
					Options: map[string]interface{}{
						"enabled":     false,
						"url":         false,
						"author_name": "Kapacitor",
					},
					Redacted: []string{
						"url",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/talk/"},
				Options: map[string]interface{}{
					"enabled":     false,
					"url":         false,
					"author_name": "Kapacitor",
				},
				Redacted: []string{
					"url",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"enabled": true,
							"url":     "http://talk.example.com/secret-token",
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/talk"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/talk/"},
							Options: map[string]interface{}{
								"enabled":     true,
								"url":         true,
								"author_name": "Kapacitor",
							},
							Redacted: []string{
								"url",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/talk/"},
						Options: map[string]interface{}{
							"enabled":     true,
							"url":         true,
							"author_name": "Kapacitor",
						},
						Redacted: []string{
							"url",
						},
					},
				},
			},
		},
		{
			section: "telegram",
			setDefaults: func(c *server.Config) {
				c.Telegram.ChatId = "kapacitor"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/telegram"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/telegram/"},
					Options: map[string]interface{}{
						"chat-id":                  "kapacitor",
						"disable-notification":     false,
						"disable-web-page-preview": false,
						"enabled":                  false,
						"global":                   false,
						"parse-mode":               "",
						"state-changes-only":       false,
						"token":                    false,
						"url":                      telegram.DefaultTelegramURL,
					},
					Redacted: []string{
						"token",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/telegram/"},
				Options: map[string]interface{}{
					"chat-id":                  "kapacitor",
					"disable-notification":     false,
					"disable-web-page-preview": false,
					"enabled":                  false,
					"global":                   false,
					"parse-mode":               "",
					"state-changes-only":       false,
					"token":                    false,
					"url":                      telegram.DefaultTelegramURL,
				},
				Redacted: []string{
					"token",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"enabled": true,
							"token":   "token",
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/telegram"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/telegram/"},
							Options: map[string]interface{}{
								"chat-id":                  "kapacitor",
								"disable-notification":     false,
								"disable-web-page-preview": false,
								"enabled":                  true,
								"global":                   false,
								"parse-mode":               "",
								"state-changes-only":       false,
								"token":                    true,
								"url":                      telegram.DefaultTelegramURL,
							},
							Redacted: []string{
								"token",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/telegram/"},
						Options: map[string]interface{}{
							"chat-id":                  "kapacitor",
							"disable-notification":     false,
							"disable-web-page-preview": false,
							"enabled":                  true,
							"global":                   false,
							"parse-mode":               "",
							"state-changes-only":       false,
							"token":                    true,
							"url":                      telegram.DefaultTelegramURL,
						},
						Redacted: []string{
							"token",
						},
					},
				},
			},
		},
		{
			section: "victorops",
			setDefaults: func(c *server.Config) {
				c.VictorOps.RoutingKey = "test"
				c.VictorOps.APIKey = "secret"
			},
			expDefaultSection: client.ConfigSection{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/victorops"},
				Elements: []client.ConfigElement{{
					Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/victorops/"},
					Options: map[string]interface{}{
						"api-key":     true,
						"enabled":     false,
						"global":      false,
						"routing-key": "test",
						"url":         victorops.DefaultVictorOpsAPIURL,
					},
					Redacted: []string{
						"api-key",
					},
				}},
			},
			expDefaultElement: client.ConfigElement{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/victorops/"},
				Options: map[string]interface{}{
					"api-key":     true,
					"enabled":     false,
					"global":      false,
					"routing-key": "test",
					"url":         victorops.DefaultVictorOpsAPIURL,
				},
				Redacted: []string{
					"api-key",
				},
			},
			updates: []updateAction{
				{
					updateAction: client.ConfigUpdateAction{
						Set: map[string]interface{}{
							"api-key": "",
							"global":  true,
						},
					},
					expSection: client.ConfigSection{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/victorops"},
						Elements: []client.ConfigElement{{
							Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/victorops/"},
							Options: map[string]interface{}{
								"api-key":     false,
								"enabled":     false,
								"global":      true,
								"routing-key": "test",
								"url":         victorops.DefaultVictorOpsAPIURL,
							},
							Redacted: []string{
								"api-key",
							},
						}},
					},
					expElement: client.ConfigElement{
						Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/config/victorops/"},
						Options: map[string]interface{}{
							"api-key":     false,
							"enabled":     false,
							"global":      true,
							"routing-key": "test",
							"url":         victorops.DefaultVictorOpsAPIURL,
						},
						Redacted: []string{
							"api-key",
						},
					},
				},
			},
		},
	}

	compareElements := func(got, exp client.ConfigElement) error {
		if got.Link != exp.Link {
			return fmt.Errorf("elements have different links, got %v exp %v", got.Link, exp.Link)
		}
		for k, v := range exp.Options {
			if g, ok := got.Options[k]; !ok {
				return fmt.Errorf("missing option %q", k)
			} else if !reflect.DeepEqual(g, v) {
				return fmt.Errorf("unexpected config option %q got %#v exp %#v types: got %T exp %T", k, g, v, g, v)
			}
		}
		for k := range got.Options {
			if v, ok := exp.Options[k]; !ok {
				return fmt.Errorf("extra option %q with value %#v", k, v)
			}
		}
		if len(got.Redacted) != len(exp.Redacted) {
			return fmt.Errorf("unexpected element redacted lists: got %v exp %v", got.Redacted, exp.Redacted)
		}
		sort.Strings(got.Redacted)
		sort.Strings(exp.Redacted)
		for i := range exp.Redacted {
			if got.Redacted[i] != exp.Redacted[i] {
				return fmt.Errorf("unexpected element redacted lists: got %v exp %v", got.Redacted, exp.Redacted)
			}
		}
		return nil
	}
	compareSections := func(got, exp client.ConfigSection) error {
		if got.Link != exp.Link {
			return fmt.Errorf("sections have different links, got %v exp %v", got.Link, exp.Link)
		}
		if len(got.Elements) != len(exp.Elements) {
			return fmt.Errorf("sections are different lengths, got %d exp %d", len(got.Elements), len(exp.Elements))
		}
		for i := range exp.Elements {
			if err := compareElements(got.Elements[i], exp.Elements[i]); err != nil {
				return errors.Wrapf(err, "section element %d are not equal", i)
			}
		}
		return nil
	}

	validate := func(
		cli *client.Client,
		section,
		element string,
		expSection client.ConfigSection,
		expElement client.ConfigElement,
	) error {
		// Get all sections
		if config, err := cli.ConfigSections(); err != nil {
			return err
		} else {
			if err := compareSections(config.Sections[section], expSection); err != nil {
				return fmt.Errorf("%s: %v", section, err)
			}
		}
		// Get the specific section
		sectionLink := cli.ConfigSectionLink(section)
		if got, err := cli.ConfigSection(sectionLink); err != nil {
			return err
		} else {
			if err := compareSections(got, expSection); err != nil {
				return fmt.Errorf("%s: %v", section, err)
			}
		}
		elementLink := cli.ConfigElementLink(section, element)
		// Get the specific element
		if got, err := cli.ConfigElement(elementLink); err != nil {
			return err
		} else {
			if err := compareElements(got, expElement); err != nil {
				return fmt.Errorf("%s/%s: %v", section, element, err)
			}
		}
		return nil
	}

	for _, tc := range testCases {
		// Create default config
		c := NewConfig()
		if tc.setDefaults != nil {
			tc.setDefaults(c)
		}
		s := OpenServer(c)
		cli := Client(s)
		defer s.Close()

		if err := validate(cli, tc.section, tc.element, tc.expDefaultSection, tc.expDefaultElement); err != nil {
			t.Errorf("unexpected defaults for %s/%s: %v", tc.section, tc.element, err)
		}

		for i, ua := range tc.updates {
			link := cli.ConfigElementLink(tc.section, ua.element)

			if len(ua.updateAction.Add) > 0 ||
				len(ua.updateAction.Remove) > 0 {
				link = cli.ConfigSectionLink(tc.section)
			}

			if err := cli.ConfigUpdate(link, ua.updateAction); err != nil {
				t.Fatal(err)
			}
			if err := validate(cli, tc.section, ua.element, ua.expSection, ua.expElement); err != nil {
				t.Errorf("unexpected update result %d for %s/%s: %v", i, tc.section, ua.element, err)
			}
		}
	}
}
func TestServer_ListServiceTests(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()
	serviceTests, err := cli.ListServiceTests(nil)
	if err != nil {
		t.Fatal(err)
	}
	expServiceTests := client.ServiceTests{
		Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests"},
		Services: []client.ServiceTest{
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/alerta"},
				Name: "alerta",
				Options: client.ServiceTestOptions{
					"resource":    "testResource",
					"event":       "testEvent",
					"environment": "",
					"severity":    "critical",
					"group":       "testGroup",
					"value":       "testValue",
					"message":     "test alerta message",
					"origin":      "",
					"service": []interface{}{
						"testServiceA",
						"testServiceB",
					},
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/hipchat"},
				Name: "hipchat",
				Options: client.ServiceTestOptions{
					"room":    "",
					"message": "test hipchat message",
					"level":   "CRITICAL",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/influxdb"},
				Name: "influxdb",
				Options: client.ServiceTestOptions{
					"cluster": "",
				},
			},
			{
				Link:    client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/kubernetes"},
				Name:    "kubernetes",
				Options: nil,
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/opsgenie"},
				Name: "opsgenie",
				Options: client.ServiceTestOptions{
					"teams":        nil,
					"recipients":   nil,
					"message-type": "CRITICAL",
					"message":      "test opsgenie message",
					"entity-id":    "testEntityID",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/pagerduty"},
				Name: "pagerduty",
				Options: client.ServiceTestOptions{
					"incident-key": "testIncidentKey",
					"description":  "test pagerduty message",
					"level":        "CRITICAL",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/sensu"},
				Name: "sensu",
				Options: client.ServiceTestOptions{
					"name":   "testName",
					"output": "testOutput",
					"level":  "CRITICAL",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/slack"},
				Name: "slack",
				Options: client.ServiceTestOptions{
					"channel":    "",
					"icon-emoji": "",
					"level":      "CRITICAL",
					"message":    "test slack message",
					"username":   "",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/smtp"},
				Name: "smtp",
				Options: client.ServiceTestOptions{
					"to":      nil,
					"subject": "test subject",
					"body":    "test body",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/snmptrap"},
				Name: "snmptrap",
				Options: client.ServiceTestOptions{
					"trap-oid": "1.1.1.1",
					"data-list": []interface{}{
						map[string]interface{}{
							"oid":   "1.1.1.1.2",
							"type":  "s",
							"value": "test snmptrap message",
						},
					},
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/talk"},
				Name: "talk",
				Options: client.ServiceTestOptions{
					"title": "testTitle",
					"text":  "test talk text",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/telegram"},
				Name: "telegram",
				Options: client.ServiceTestOptions{
					"chat-id":                  "",
					"parse-mode":               "",
					"message":                  "test telegram message",
					"disable-web-page-preview": false,
					"disable-notification":     false,
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/victorops"},
				Name: "victorops",
				Options: client.ServiceTestOptions{
					"routingKey":  "",
					"messageType": "CRITICAL",
					"message":     "test victorops message",
					"entityID":    "testEntityID",
				},
			},
		},
	}
	if got, exp := serviceTests.Link.Href, expServiceTests.Link.Href; got != exp {
		t.Errorf("unexpected service tests link.href: got %s exp %s", got, exp)
	}
	if got, exp := len(serviceTests.Services), len(expServiceTests.Services); got != exp {
		t.Fatalf("unexpected length of services: got %d exp %d", got, exp)
	}
	for i := range expServiceTests.Services {
		exp := expServiceTests.Services[i]
		got := serviceTests.Services[i]
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("unexpected server test %s:\ngot\n%#v\nexp\n%#v\n", exp.Name, got, exp)
		}
	}
}

func TestServer_ListServiceTests_WithPattern(t *testing.T) {
	s, cli := OpenDefaultServer()
	defer s.Close()
	serviceTests, err := cli.ListServiceTests(&client.ListServiceTestsOptions{
		Pattern: "s*",
	})
	if err != nil {
		t.Fatal(err)
	}
	expServiceTests := client.ServiceTests{
		Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests"},
		Services: []client.ServiceTest{
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/sensu"},
				Name: "sensu",
				Options: client.ServiceTestOptions{
					"name":   "testName",
					"output": "testOutput",
					"level":  "CRITICAL",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/slack"},
				Name: "slack",
				Options: client.ServiceTestOptions{
					"channel":    "",
					"icon-emoji": "",
					"level":      "CRITICAL",
					"message":    "test slack message",
					"username":   "",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/smtp"},
				Name: "smtp",
				Options: client.ServiceTestOptions{
					"to":      nil,
					"subject": "test subject",
					"body":    "test body",
				},
			},
			{
				Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1/service-tests/snmptrap"},
				Name: "snmptrap",
				Options: client.ServiceTestOptions{
					"trap-oid": "1.1.1.1",
					"data-list": []interface{}{
						map[string]interface{}{
							"oid":   "1.1.1.1.2",
							"type":  "s",
							"value": "test snmptrap message",
						},
					},
				},
			},
		},
	}
	if got, exp := serviceTests.Link.Href, expServiceTests.Link.Href; got != exp {
		t.Errorf("unexpected service tests link.href: got %s exp %s", got, exp)
	}
	if got, exp := len(serviceTests.Services), len(expServiceTests.Services); got != exp {
		t.Fatalf("unexpected length of services: got %d exp %d", got, exp)
	}
	for i := range expServiceTests.Services {
		exp := expServiceTests.Services[i]
		got := serviceTests.Services[i]
		if !reflect.DeepEqual(got, exp) {
			t.Errorf("unexpected server test %s:\ngot\n%#v\nexp\n%#v\n", exp.Name, got, exp)
		}
	}
}

func TestServer_DoServiceTest(t *testing.T) {
	db := NewInfluxDB(func(q string) *iclient.Response {
		return &iclient.Response{}
	})
	testCases := []struct {
		service     string
		setDefaults func(*server.Config)
		options     client.ServiceTestOptions
		exp         client.ServiceTestResult
	}{
		{
			service: "alerta",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "hipchat",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "influxdb",
			setDefaults: func(c *server.Config) {
				c.InfluxDB[0].Enabled = true
				c.InfluxDB[0].Name = "default"
				c.InfluxDB[0].URLs = []string{db.URL()}
			},
			options: client.ServiceTestOptions{
				"cluster": "default",
			},
			exp: client.ServiceTestResult{
				Success: true,
				Message: "",
			},
		},
		{
			service: "influxdb",
			options: client.ServiceTestOptions{
				"cluster": "default",
			},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "cluster \"default\" is not enabled or does not exist",
			},
		},
		{
			service: "kubernetes",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "failed to get client: service is not enabled",
			},
		},
		{
			service: "opsgenie",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "pagerduty",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "sensu",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "slack",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "smtp",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "snmptrap",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "talk",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "telegram",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
		{
			service: "victorops",
			options: client.ServiceTestOptions{},
			exp: client.ServiceTestResult{
				Success: false,
				Message: "service is not enabled",
			},
		},
	}

	for _, tc := range testCases {
		// Create default config
		c := NewConfig()
		if tc.setDefaults != nil {
			tc.setDefaults(c)
		}
		s := OpenServer(c)
		cli := Client(s)
		defer s.Close()

		tr, err := cli.DoServiceTest(cli.ServiceTestLink(tc.service), tc.options)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(tr, tc.exp) {
			t.Log("Options", tc.options)
			t.Errorf("unexpected service test result for %s:\ngot\n%#v\nexp\n%#v\n", tc.service, tr, tc.exp)
		}
	}
}

func TestServer_AlertHandlers_CRUD(t *testing.T) {
	testCases := []struct {
		create    client.HandlerOptions
		expCreate client.Handler
		patch     client.JSONPatch
		expPatch  client.Handler
		put       client.HandlerOptions
		expPut    client.Handler
	}{
		{
			create: client.HandlerOptions{
				ID:     "myhandler",
				Topics: []string{"system", "test"},
				Actions: []client.HandlerAction{{
					Kind: "slack",
					Options: map[string]interface{}{
						"channel": "#test",
					},
				}},
			},
			expCreate: client.Handler{
				Link:   client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/myhandler"},
				ID:     "myhandler",
				Topics: []string{"system", "test"},
				Actions: []client.HandlerAction{{
					Kind: "slack",
					Options: map[string]interface{}{
						"channel": "#test",
					},
				}},
			},
			patch: client.JSONPatch{
				{
					Path:      "/topics/0",
					Operation: "remove",
				},
				{
					Path:      "/actions/0/options/channel",
					Operation: "replace",
					Value:     "#kapacitor_test",
				},
			},
			expPatch: client.Handler{
				Link:   client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/myhandler"},
				ID:     "myhandler",
				Topics: []string{"test"},
				Actions: []client.HandlerAction{{
					Kind: "slack",
					Options: map[string]interface{}{
						"channel": "#kapacitor_test",
					},
				}},
			},
			put: client.HandlerOptions{
				ID:     "newid",
				Topics: []string{"test"},
				Actions: []client.HandlerAction{{
					Kind: "smtp",
					Options: map[string]interface{}{
						"to": []string{"oncall@example.com"},
					},
				}},
			},
			expPut: client.Handler{
				Link:   client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/newid"},
				ID:     "newid",
				Topics: []string{"test"},
				Actions: []client.HandlerAction{{
					Kind: "smtp",
					Options: map[string]interface{}{
						"to": []interface{}{"oncall@example.com"},
					},
				}},
			},
		},
		{
			create: client.HandlerOptions{
				ID:     "anotherhandler",
				Topics: []string{"test"},
				Actions: []client.HandlerAction{
					{
						Kind: "slack",
						Options: map[string]interface{}{
							"channel": "#test",
						},
					},
					{
						Kind: "log",
						Options: map[string]interface{}{
							"path": "/tmp/alert.log",
						},
					},
				},
			},
			expCreate: client.Handler{
				Link:   client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/anotherhandler"},
				ID:     "anotherhandler",
				Topics: []string{"test"},
				Actions: []client.HandlerAction{
					{
						Kind: "slack",
						Options: map[string]interface{}{
							"channel": "#test",
						},
					},
					{
						Kind: "log",
						Options: map[string]interface{}{
							"path": "/tmp/alert.log",
						},
					},
				},
			},
			patch: client.JSONPatch{
				{
					Path:      "/topics/-",
					Operation: "add",
					Value:     "system",
				},
				{
					Path:      "/actions/0/options/channel",
					Operation: "replace",
					Value:     "#kapacitor_test",
				},
				{
					Path:      "/actions/-",
					Operation: "add",
					Value: map[string]interface{}{
						"kind": "smtp",
						"options": map[string]interface{}{
							"to": []string{"oncall@example.com"},
						},
					},
				},
			},
			expPatch: client.Handler{
				Link:   client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/anotherhandler"},
				ID:     "anotherhandler",
				Topics: []string{"test", "system"},
				Actions: []client.HandlerAction{
					{
						Kind: "slack",
						Options: map[string]interface{}{
							"channel": "#kapacitor_test",
						},
					},
					{
						Kind: "log",
						Options: map[string]interface{}{
							"path": "/tmp/alert.log",
						},
					},
					{
						Kind: "smtp",
						Options: map[string]interface{}{
							"to": []interface{}{"oncall@example.com"},
						},
					},
				},
			},
			put: client.HandlerOptions{
				ID:     "anotherhandler",
				Topics: []string{"test"},
				Actions: []client.HandlerAction{{
					Kind: "smtp",
					Options: map[string]interface{}{
						"to": []string{"oncall@example.com"},
					},
				}},
			},
			expPut: client.Handler{
				Link:   client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/anotherhandler"},
				ID:     "anotherhandler",
				Topics: []string{"test"},
				Actions: []client.HandlerAction{{
					Kind: "smtp",
					Options: map[string]interface{}{
						"to": []interface{}{"oncall@example.com"},
					},
				}},
			},
		},
	}
	for _, tc := range testCases {
		// Create default config
		c := NewConfig()
		s := OpenServer(c)
		cli := Client(s)
		defer s.Close()

		h, err := cli.CreateHandler(tc.create)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(h, tc.expCreate) {
			t.Errorf("unexpected handler created:\ngot\n%#v\nexp\n%#v\n", h, tc.expCreate)
		}

		h, err = cli.PatchHandler(h.Link, tc.patch)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(h, tc.expPatch) {
			t.Errorf("unexpected handler patched:\ngot\n%#v\nexp\n%#v\n", h, tc.expPatch)
		}

		h, err = cli.ReplaceHandler(h.Link, tc.put)
		if err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(h, tc.expPut) {
			t.Errorf("unexpected handler put:\ngot\n%#v\nexp\n%#v\n", h, tc.expPut)
		}

		// Restart server
		s.Restart()

		rh, err := cli.Handler(h.Link)
		if err != nil {
			t.Fatalf("could not find handler after restart: %v", err)
		}
		if got, exp := rh, h; !reflect.DeepEqual(got, exp) {
			t.Errorf("unexpected handler after restart:\ngot\n%#v\nexp\n%#v\n", got, exp)
		}

		err = cli.DeleteHandler(h.Link)
		if err != nil {
			t.Fatal(err)
		}

		_, err = cli.Handler(h.Link)
		if err == nil {
			t.Errorf("expected handler to be deleted")
		}
	}
}

func TestServer_AlertHandlers(t *testing.T) {

	resultJSON := `{"Series":[{"name":"alert","columns":["time","value"],"values":[["1970-01-01T00:00:00Z",1]]}],"Messages":null,"Err":null}`

	alertData := alertservice.AlertData{
		ID:      "id",
		Message: "message",
		Details: "details",
		Time:    time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		Level:   alert.Critical,
		Data: influxql.Result{
			Series: models.Rows{
				{
					Name:    "alert",
					Columns: []string{"time", "value"},
					Values: [][]interface{}{[]interface{}{
						time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
						1.0,
					}},
				},
			},
		},
	}
	adJSON, err := json.Marshal(alertData)
	if err != nil {
		t.Fatal(err)
	}
	testCases := []struct {
		handlerAction client.HandlerAction
		setup         func(*server.Config, *client.HandlerAction) (context.Context, error)
		result        func(context.Context) error
	}{
		{
			handlerAction: client.HandlerAction{
				Kind: "alerta",
				Options: map[string]interface{}{
					"token":       "testtoken1234567",
					"origin":      "kapacitor",
					"group":       "test",
					"environment": "env",
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := alertatest.NewServer()
				ctxt := context.WithValue(nil, "server", ts)

				c.Alerta.Enabled = true
				c.Alerta.URL = ts.URL
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*alertatest.Server)
				ts.Close()
				got := ts.Requests()
				exp := []alertatest.Request{{
					URL:           "/alert",
					Authorization: "Key testtoken1234567",
					PostData: alertatest.PostData{
						Resource:    "alert",
						Event:       "id",
						Group:       "test",
						Environment: "env",
						Text:        "message",
						Origin:      "kapacitor",
						Service:     []string{"alert"},
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected alerta request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "exec",
				Options: map[string]interface{}{
					"prog": "/bin/alert-handler.sh",
					"args": []string{"arg1", "arg2", "arg3"},
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				te := alerttest.NewExec()
				ctxt := context.WithValue(nil, "exec", te)
				c.Commander = te.Commander
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				te := ctxt.Value("exec").(*alerttest.Exec)
				expData := []*commandtest.Command{{
					Spec: command.Spec{
						Prog: "/bin/alert-handler.sh",
						Args: []string{"arg1", "arg2", "arg3"},
					},
					Started:   true,
					Waited:    true,
					Killed:    false,
					StdinData: append(adJSON, '\n'),
				}}
				cmds := te.Commands()
				if got, exp := len(cmds), len(expData); got != exp {
					return fmt.Errorf("unexpected commands length: got %d exp %d", got, exp)
				}
				for i := range expData {
					if err := expData[i].Compare(cmds[i]); err != nil {
						return fmt.Errorf("unexpected command %d: %v", i, err)
					}
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "hipchat",
				Options: map[string]interface{}{
					"token": "testtoken1234567",
					"room":  "1234567",
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := hipchattest.NewServer()
				ctxt := context.WithValue(nil, "server", ts)

				c.HipChat.Enabled = true
				c.HipChat.URL = ts.URL
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*hipchattest.Server)
				ts.Close()
				got := ts.Requests()
				exp := []hipchattest.Request{{
					URL: "/1234567/notification?auth_token=testtoken1234567",
					PostData: hipchattest.PostData{
						From:    "kapacitor",
						Message: "message",
						Color:   "red",
						Notify:  true,
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected hipchat request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "log",
				Options: map[string]interface{}{
					"mode": 0604,
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				tdir := MustTempDir()
				p := path.Join(tdir, "alert.log")

				ha.Options["path"] = p

				l := alerttest.NewLog(p)

				ctxt := context.WithValue(nil, "tdir", tdir)
				ctxt = context.WithValue(ctxt, "log", l)
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				tdir := ctxt.Value("tdir").(string)
				defer os.RemoveAll(tdir)
				l := ctxt.Value("log").(*alerttest.Log)
				expData := []alertservice.AlertData{alertData}
				expMode := os.FileMode(0604)

				m, err := l.Mode()
				if err != nil {
					return err
				}
				if got, exp := m, expMode; exp != got {
					return fmt.Errorf("unexpected file mode: got %v exp %v", got, exp)
				}
				data, err := l.Data()
				if err != nil {
					return err
				}
				if got, exp := data, expData; !reflect.DeepEqual(got, exp) {
					return fmt.Errorf("unexpected alert data written to log:\ngot\n%+v\nexp\n%+v\n", got, exp)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "opsgenie",
				Options: map[string]interface{}{
					"teams-list":      []string{"A team", "B team"},
					"recipients-list": []string{"test_recipient1", "test_recipient2"},
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := opsgenietest.NewServer()
				ctxt := context.WithValue(nil, "server", ts)

				c.OpsGenie.Enabled = true
				c.OpsGenie.URL = ts.URL
				c.OpsGenie.APIKey = "api_key"
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*opsgenietest.Server)
				ts.Close()
				got := ts.Requests()
				exp := []opsgenietest.Request{{
					URL: "/",
					PostData: opsgenietest.PostData{
						ApiKey:  "api_key",
						Message: "message",
						Entity:  "id",
						Alias:   "id",
						Note:    "",
						Details: map[string]interface{}{
							"Level":           "CRITICAL",
							"Monitoring Tool": "Kapacitor",
						},
						Description: resultJSON,
						Teams:       []string{"A team", "B team"},
						Recipients:  []string{"test_recipient1", "test_recipient2"},
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected opsgenie request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "pagerduty",
				Options: map[string]interface{}{
					"service-key": "service_key",
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := pagerdutytest.NewServer()
				ctxt := context.WithValue(nil, "server", ts)

				c.PagerDuty.Enabled = true
				c.PagerDuty.URL = ts.URL
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*pagerdutytest.Server)
				kapacitorURL := ctxt.Value("kapacitorURL").(string)
				ts.Close()
				got := ts.Requests()
				exp := []pagerdutytest.Request{{
					URL: "/",
					PostData: pagerdutytest.PostData{
						ServiceKey:  "service_key",
						EventType:   "trigger",
						Description: "message",
						Client:      "kapacitor",
						ClientURL:   kapacitorURL,
						Details:     resultJSON,
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected pagerduty request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "post",
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := alerttest.NewPostServer()

				ha.Options = map[string]interface{}{"url": ts.URL}

				ctxt := context.WithValue(nil, "server", ts)
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*alerttest.PostServer)
				ts.Close()
				exp := []alertservice.AlertData{alertData}
				got := ts.Data()
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected post request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "sensu",
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts, err := sensutest.NewServer()
				if err != nil {
					return nil, err
				}
				ctxt := context.WithValue(nil, "server", ts)

				c.Sensu.Enabled = true
				c.Sensu.Addr = ts.Addr
				c.Sensu.Source = "Kapacitor"
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*sensutest.Server)
				ts.Close()
				exp := []sensutest.Request{{
					Source: "Kapacitor",
					Output: "message",
					Name:   "id",
					Status: 2,
				}}
				got := ts.Requests()
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected sensu request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "slack",
				Options: map[string]interface{}{
					"channel": "#test",
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := slacktest.NewServer()
				ctxt := context.WithValue(nil, "server", ts)

				c.Slack.Enabled = true
				c.Slack.URL = ts.URL + "/test/slack/url"
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*slacktest.Server)
				ts.Close()
				got := ts.Requests()
				exp := []slacktest.Request{{
					URL: "/test/slack/url",
					PostData: slacktest.PostData{
						Channel:  "#test",
						Username: "kapacitor",
						Text:     "",
						Attachments: []slacktest.Attachment{
							{
								Fallback:  "message",
								Color:     "danger",
								Text:      "message",
								Mrkdwn_in: []string{"text"},
							},
						},
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected slack request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "smtp",
				Options: map[string]interface{}{
					"to": []string{"oncall@example.com", "backup@example.com"},
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts, err := smtptest.NewServer()
				if err != nil {
					return nil, err
				}
				ctxt := context.WithValue(nil, "server", ts)

				c.SMTP.Enabled = true
				c.SMTP.Host = ts.Host
				c.SMTP.Port = ts.Port
				c.SMTP.From = "test@example.com"
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*smtptest.Server)
				ts.Close()

				errors := ts.Errors()
				if len(errors) != 0 {
					return fmt.Errorf("multiple errors %d: %v", len(errors), errors)
				}

				expMail := []*smtptest.Message{{
					Header: mail.Header{
						"Mime-Version":              []string{"1.0"},
						"Content-Type":              []string{"text/html; charset=UTF-8"},
						"Content-Transfer-Encoding": []string{"quoted-printable"},
						"To":      []string{"oncall@example.com, backup@example.com"},
						"From":    []string{"test@example.com"},
						"Subject": []string{"message"},
					},
					Body: "details\n",
				}}
				msgs := ts.SentMessages()
				if got, exp := len(msgs), len(expMail); got != exp {
					return fmt.Errorf("unexpected number of messages sent: got %d exp %d", got, exp)
				}
				for i, exp := range expMail {
					got := msgs[i]
					if err := exp.Compare(got); err != nil {
						return fmt.Errorf("unexpected message %d: %v", i, err)
					}
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "snmptrap",
				Options: map[string]interface{}{
					"trap-oid": "1.1.2",
					"data-list": []map[string]string{
						{
							"oid":   "1.1.2.1",
							"type":  "s",
							"value": "{{.Message}}",
						},
						{
							"oid":   "1.1.2.2",
							"type":  "s",
							"value": "{{.Level}}",
						},
					},
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts, err := snmptraptest.NewServer()
				if err != nil {
					return nil, err
				}
				ctxt := context.WithValue(nil, "server", ts)

				c.SNMPTrap.Enabled = true
				c.SNMPTrap.Addr = ts.Addr
				c.SNMPTrap.Community = ts.Community
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*snmptraptest.Server)
				ts.Close()
				got := ts.Traps()
				exp := []snmptraptest.Trap{{
					Pdu: snmptraptest.Pdu{
						Type:        snmpgo.SNMPTrapV2,
						ErrorStatus: snmpgo.NoError,
						VarBinds: snmptraptest.VarBinds{
							{
								Oid:   "1.3.6.1.6.3.1.1.4.1.0",
								Value: "1.1.2",
								Type:  "Oid",
							},
							{
								Oid:   "1.1.2.1",
								Value: "message",
								Type:  "OctetString",
							},
							{
								Oid:   "1.1.2.2",
								Value: "CRITICAL",
								Type:  "OctetString",
							},
						},
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected snmptrap request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "talk",
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := talktest.NewServer()
				ctxt := context.WithValue(nil, "server", ts)

				c.Talk.Enabled = true
				c.Talk.URL = ts.URL
				c.Talk.AuthorName = "Kapacitor"
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*talktest.Server)
				ts.Close()
				got := ts.Requests()
				exp := []talktest.Request{{
					URL: "/",
					PostData: talktest.PostData{
						AuthorName: "Kapacitor",
						Text:       "message",
						Title:      "id",
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected talk request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "tcp",
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts, err := alerttest.NewTCPServer()
				if err != nil {
					return nil, err
				}

				ha.Options = map[string]interface{}{"address": ts.Addr}

				ctxt := context.WithValue(nil, "server", ts)
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*alerttest.TCPServer)
				ts.Close()
				exp := []alertservice.AlertData{alertData}
				got := ts.Data()
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected tcp request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "telegram",
				Options: map[string]interface{}{
					"chat-id":                  "chat id",
					"disable-web-page-preview": true,
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := telegramtest.NewServer()
				ctxt := context.WithValue(nil, "server", ts)

				c.Telegram.Enabled = true
				c.Telegram.URL = ts.URL + "/bot"
				c.Telegram.Token = "TOKEN:AUTH"
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*telegramtest.Server)
				ts.Close()
				got := ts.Requests()
				exp := []telegramtest.Request{{
					URL: "/botTOKEN:AUTH/sendMessage",
					PostData: telegramtest.PostData{
						ChatId:                "chat id",
						Text:                  "message",
						ParseMode:             "",
						DisableWebPagePreview: true,
						DisableNotification:   false,
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected telegram request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
		{
			handlerAction: client.HandlerAction{
				Kind: "victorops",
				Options: map[string]interface{}{
					"routing-key": "key",
				},
			},
			setup: func(c *server.Config, ha *client.HandlerAction) (context.Context, error) {
				ts := victoropstest.NewServer()
				ctxt := context.WithValue(nil, "server", ts)

				c.VictorOps.Enabled = true
				c.VictorOps.URL = ts.URL
				c.VictorOps.APIKey = "api_key"
				return ctxt, nil
			},
			result: func(ctxt context.Context) error {
				ts := ctxt.Value("server").(*victoropstest.Server)
				ts.Close()
				got := ts.Requests()
				exp := []victoropstest.Request{{
					URL: "/api_key/key",
					PostData: victoropstest.PostData{
						MessageType:    "CRITICAL",
						EntityID:       "id",
						StateMessage:   "message",
						Timestamp:      0,
						MonitoringTool: "kapacitor",
						Data:           resultJSON,
					},
				}}
				if !reflect.DeepEqual(exp, got) {
					return fmt.Errorf("unexpected victorops request:\nexp\n%+v\ngot\n%+v\n", exp, got)
				}
				return nil
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.handlerAction.Kind, func(t *testing.T) {
			kind := tc.handlerAction.Kind
			// Create default config
			c := NewConfig()
			var ctxt context.Context
			if tc.setup != nil {
				var err error
				ctxt, err = tc.setup(c, &tc.handlerAction)
				if err != nil {
					t.Fatal(err)
				}
			}
			s := OpenServer(c)
			cli := Client(s)
			closed := false
			defer func() {
				if !closed {
					s.Close()
				}
			}()
			ctxt = context.WithValue(ctxt, "kapacitorURL", s.URL())

			if _, err := cli.CreateHandler(client.HandlerOptions{
				ID:     "testAlertHandlers",
				Topics: []string{"test"},
				Actions: []client.HandlerAction{
					tc.handlerAction,
				},
			}); err != nil {
				t.Fatalf("%s: %v", kind, err)
			}

			tick := `
stream
	|from()
		.measurement('alert')
	|alert()
		.topic('test')
		.id('id')
		.message('message')
		.details('details')
		.crit(lambda: TRUE)
`

			if _, err := cli.CreateTask(client.CreateTaskOptions{
				ID:   "testAlertHandlers",
				Type: client.StreamTask,
				DBRPs: []client.DBRP{{
					Database:        "mydb",
					RetentionPolicy: "myrp",
				}},
				TICKscript: tick,
				Status:     client.Enabled,
			}); err != nil {
				t.Fatalf("%s: %v", kind, err)
			}

			point := "alert value=1 0000000000"
			v := url.Values{}
			v.Add("precision", "s")
			s.MustWrite("mydb", "myrp", point, v)

			// Close the entire server to ensure all data is processed
			s.Close()
			closed = true

			if err := tc.result(ctxt); err != nil {
				t.Errorf("%s: %v", kind, err)
			}
		})
	}
}

func TestServer_AlertAnonTopic(t *testing.T) {
	// Setup test TCP server
	ts, err := alerttest.NewTCPServer()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	// Create default config
	c := NewConfig()
	s := OpenServer(c)
	cli := Client(s)
	defer s.Close()

	tick := `
stream
	|from()
		.measurement('alert')
	|alert()
		.id('id')
		.message('message')
		.details('details')
		.warn(lambda: "value" <= 1.0)
		.crit(lambda: "value" > 1.0)
		.tcp('` + ts.Addr + `')
`

	task, err := cli.CreateTask(client.CreateTaskOptions{
		ID:   "testAlertHandlers",
		Type: client.StreamTask,
		DBRPs: []client.DBRP{{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		}},
		TICKscript: tick,
		Status:     client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Write warning point
	point := "alert value=1 0000000000"
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", point, v)

	// Restart the server
	s.Restart()

	topic := "main:testAlertHandlers:alert2"
	l := cli.TopicEventsLink(topic)
	expTopicEvents := client.TopicEvents{
		Link:  l,
		Topic: topic,
		Events: []client.TopicEvent{{
			Link: client.Link{Relation: client.Self, Href: fmt.Sprintf("/kapacitor/v1preview/alerts/topics/%s/events/id", topic)},
			ID:   "id",
			State: client.EventState{
				Message:  "message",
				Details:  "details",
				Time:     time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
				Duration: 0,
				Level:    "WARNING",
			},
		}},
	}

	te, err := cli.ListTopicEvents(l, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(te, expTopicEvents) {
		t.Errorf("unexpected topic events for anonymous topic:\ngot\n%+v\nexp\n%+v\n", te, expTopicEvents)
	}
	event, err := cli.TopicEvent(expTopicEvents.Events[0].Link)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(event, expTopicEvents.Events[0]) {
		t.Errorf("unexpected topic event for anonymous topic:\ngot\n%+v\nexp\n%+v\n", event, expTopicEvents.Events[0])
	}

	// Disable task
	task, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Disabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := cli.ListTopicEvents(l, nil); err == nil {
		t.Fatal("expected error listing anonymous topic for disabled task")
	} else if got, exp := err.Error(), fmt.Sprintf("topic %q does not exist", topic); got != exp {
		t.Errorf("unexpected error message for nonexistent anonymous topic: got %q exp %q", got, exp)
	}

	// Enable task
	task, err = cli.UpdateTask(task.Link, client.UpdateTaskOptions{
		Status: client.Enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	te, err = cli.ListTopicEvents(l, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(te, expTopicEvents) {
		t.Errorf("unexpected topic events for anonymous topic after re-enable:\ngot\n%+v\nexp\n%+v\n", te, expTopicEvents)
	}

	// Restart the server, again and ensure that the anonymous topic state is restored
	s.Restart()
	te, err = cli.ListTopicEvents(l, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(te, expTopicEvents) {
		t.Errorf("unexpected topic events for anonymous topic after re-enable and restart:\ngot\n%+v\nexp\n%+v\n", te, expTopicEvents)
	}

	// Delete task
	if err := cli.DeleteTask(task.Link); err != nil {
		t.Fatal(err)
	}

	if _, err := cli.ListTopicEvents(l, nil); err == nil {
		t.Fatal("expected error listing anonymous topic for deleted task")
	} else if got, exp := err.Error(), fmt.Sprintf("topic %q does not exist", topic); got != exp {
		t.Errorf("unexpected error message for nonexistent anonymous topic: got %q exp %q", got, exp)
	}
}

func TestServer_AlertTopic_PersistedState(t *testing.T) {
	// Setup test TCP server
	ts, err := alerttest.NewTCPServer()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	tmpDir := MustTempDir()
	defer os.RemoveAll(tmpDir)
	tmpPath := path.Join(tmpDir, "alert.log")

	// Create default config
	c := NewConfig()
	s := OpenServer(c)
	cli := Client(s)
	defer s.Close()

	if _, err := cli.CreateHandler(client.HandlerOptions{
		ID:     "testAlertHandler",
		Topics: []string{"test"},
		Actions: []client.HandlerAction{{
			Kind:    "tcp",
			Options: map[string]interface{}{"address": ts.Addr},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	tick := `
stream
	|from()
		.measurement('alert')
	|alert()
		.topic('test')
		.id('id')
		.message('message')
		.details('details')
		.warn(lambda: TRUE)
		.log('` + tmpPath + `')
`

	if _, err := cli.CreateTask(client.CreateTaskOptions{
		ID:   "testAlertHandlers",
		Type: client.StreamTask,
		DBRPs: []client.DBRP{{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		}},
		TICKscript: tick,
		Status:     client.Enabled,
	}); err != nil {
		t.Fatal(err)
	}

	point := "alert value=1 0000000000"
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", point, v)

	// Restart the server
	s.Restart()

	topics := []string{
		"test",
		"main:testAlertHandlers:alert2",
	}
	for _, topic := range topics {
		l := cli.TopicEventsLink(topic)
		expTopicEvents := client.TopicEvents{
			Link:  l,
			Topic: topic,
			Events: []client.TopicEvent{{
				Link: client.Link{Relation: client.Self, Href: fmt.Sprintf("/kapacitor/v1preview/alerts/topics/%s/events/id", topic)},
				ID:   "id",
				State: client.EventState{
					Message:  "message",
					Details:  "details",
					Time:     time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
					Duration: 0,
					Level:    "WARNING",
				},
			}},
		}

		te, err := cli.ListTopicEvents(l, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(te, expTopicEvents) {
			t.Errorf("unexpected topic events for topic %q:\ngot\n%+v\nexp\n%+v\n", topic, te, expTopicEvents)
		}
		event, err := cli.TopicEvent(expTopicEvents.Events[0].Link)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(event, expTopicEvents.Events[0]) {
			t.Errorf("unexpected topic event for topic %q:\ngot\n%+v\nexp\n%+v\n", topic, event, expTopicEvents.Events[0])
		}

		te, err = cli.ListTopicEvents(l, &client.ListTopicEventsOptions{
			MinLevel: "CRITICAL",
		})
		if err != nil {
			t.Fatal(err)
		}
		expTopicEvents.Events = expTopicEvents.Events[0:0]
		if !reflect.DeepEqual(te, expTopicEvents) {
			t.Errorf("unexpected topic events with minLevel for topic %q:\ngot\n%+v\nexp\n%+v\n", topic, te, expTopicEvents)
		}

		l = cli.TopicLink(topic)
		if err := cli.DeleteTopic(l); err != nil {
			t.Fatal(err)
		}
		te, err = cli.ListTopicEvents(l, nil)
		if err == nil {
			t.Fatalf("expected error for deleted topic %q", topic)
		}
	}
}

func TestServer_AlertListHandlers(t *testing.T) {
	// Setup test TCP server
	ts, err := alerttest.NewTCPServer()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	// Create default config
	c := NewConfig()
	s := OpenServer(c)
	cli := Client(s)
	defer s.Close()

	topics := []string{"test"}
	actions := []client.HandlerAction{{
		Kind:    "tcp",
		Options: map[string]interface{}{"address": ts.Addr},
	}}

	// Number of handlers to create
	n := 3
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("handler%d", i)
		if _, err := cli.CreateHandler(client.HandlerOptions{
			ID:      id,
			Topics:  topics,
			Actions: actions,
		}); err != nil {
			t.Fatal(err)
		}
	}

	expHandlers := client.Handlers{
		Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers?pattern="},
		Handlers: []client.Handler{
			{
				Link:    client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/handler0"},
				ID:      "handler0",
				Topics:  topics,
				Actions: actions,
			},
			{
				Link:    client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/handler1"},
				ID:      "handler1",
				Topics:  topics,
				Actions: actions,
			},
			{
				Link:    client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/handlers/handler2"},
				ID:      "handler2",
				Topics:  topics,
				Actions: actions,
			},
		},
	}

	handlers, err := cli.ListHandlers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(handlers, expHandlers) {
		t.Errorf("unexpected handlers:\ngot\n%+v\nexp\n%+v\n", handlers, expHandlers)
	}

	// Restart the server
	s.Restart()

	// Check again
	handlers, err = cli.ListHandlers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(handlers, expHandlers) {
		t.Errorf("unexpected handlers after restart:\ngot\n%+v\nexp\n%+v\n", handlers, expHandlers)
	}

	var exp client.Handlers

	// Pattern = *
	handlers, err = cli.ListHandlers(&client.ListHandlersOptions{
		Pattern: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	exp = expHandlers
	exp.Link.Href = "/kapacitor/v1preview/alerts/handlers?pattern=%2A"
	if !reflect.DeepEqual(handlers, exp) {
		t.Errorf("unexpected handlers with pattern \"*\":\ngot\n%+v\nexp\n%+v\n", handlers, exp)
	}

	// Pattern = handler*
	handlers, err = cli.ListHandlers(&client.ListHandlersOptions{
		Pattern: "handler*",
	})
	if err != nil {
		t.Fatal(err)
	}
	exp = expHandlers
	exp.Link.Href = "/kapacitor/v1preview/alerts/handlers?pattern=handler%2A"
	if !reflect.DeepEqual(handlers, exp) {
		t.Errorf("unexpected handlers with pattern \"test\":\ngot\n%+v\nexp\n%+v\n", handlers, exp)
	}

	// Pattern = handler0
	handlers, err = cli.ListHandlers(&client.ListHandlersOptions{
		Pattern: "handler0",
	})
	if err != nil {
		t.Fatal(err)
	}
	exp = expHandlers
	exp.Link.Href = "/kapacitor/v1preview/alerts/handlers?pattern=handler0"
	exp.Handlers = expHandlers.Handlers[0:1]
	if !reflect.DeepEqual(handlers, exp) {
		t.Errorf("unexpected handlers with pattern \"test\":\ngot\n%+v\nexp\n%+v\n", handlers, exp)
	}

	// List handlers of test topic
	l := cli.TopicHandlersLink("test")
	topicHandlers, err := cli.ListTopicHandlers(l)
	expTopicHandlers := client.TopicHandlers{
		Link:     client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/topics/test/handlers"},
		Topic:    "test",
		Handlers: expHandlers.Handlers,
	}
	if !reflect.DeepEqual(topicHandlers, expTopicHandlers) {
		t.Errorf("unexpected topic handlers:\ngot\n%+v\nexp\n%+v\n", topicHandlers, expTopicHandlers)
	}
}
func TestServer_AlertTopic(t *testing.T) {
	// Create default config
	c := NewConfig()
	s := OpenServer(c)
	cli := Client(s)
	defer s.Close()

	if _, err := cli.CreateHandler(client.HandlerOptions{
		ID:     "testAlertHandler",
		Topics: []string{"misc"},
		Actions: []client.HandlerAction{{
			Kind:    "tcp",
			Options: map[string]interface{}{"address": "localhost:4657"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	expTopic := client.Topic{
		Link:         client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/topics/misc"},
		ID:           "misc",
		Level:        "OK",
		Collected:    0,
		EventsLink:   client.Link{Relation: "events", Href: "/kapacitor/v1preview/alerts/topics/misc/events"},
		HandlersLink: client.Link{Relation: "handlers", Href: "/kapacitor/v1preview/alerts/topics/misc/handlers"},
	}
	topic, err := cli.Topic(cli.TopicLink("misc"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(topic, expTopic) {
		t.Errorf("unexpected topic:\ngot\n%+v\nexp\n%+v\n", topic, expTopic)
	}
}

func TestServer_AlertListTopics(t *testing.T) {
	// Setup test TCP server
	ts, err := alerttest.NewTCPServer()
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	// Create default config
	c := NewConfig()
	s := OpenServer(c)
	cli := Client(s)
	defer s.Close()

	if _, err := cli.CreateHandler(client.HandlerOptions{
		ID:     "testAlertHandler",
		Topics: []string{"test", "system", "misc"},
		Actions: []client.HandlerAction{{
			Kind:    "tcp",
			Options: map[string]interface{}{"address": ts.Addr},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	expTopics := client.Topics{
		Link: client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/topics?min-level=OK&pattern="},
		Topics: []client.Topic{
			{
				Link:         client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/topics/misc"},
				ID:           "misc",
				Level:        "OK",
				EventsLink:   client.Link{Relation: "events", Href: "/kapacitor/v1preview/alerts/topics/misc/events"},
				HandlersLink: client.Link{Relation: "handlers", Href: "/kapacitor/v1preview/alerts/topics/misc/handlers"},
			},
			{
				Link:         client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/topics/system"},
				ID:           "system",
				Level:        "OK",
				EventsLink:   client.Link{Relation: "events", Href: "/kapacitor/v1preview/alerts/topics/system/events"},
				HandlersLink: client.Link{Relation: "handlers", Href: "/kapacitor/v1preview/alerts/topics/system/handlers"},
			},
			{
				Link:         client.Link{Relation: client.Self, Href: "/kapacitor/v1preview/alerts/topics/test"},
				ID:           "test",
				Level:        "OK",
				EventsLink:   client.Link{Relation: "events", Href: "/kapacitor/v1preview/alerts/topics/test/events"},
				HandlersLink: client.Link{Relation: "handlers", Href: "/kapacitor/v1preview/alerts/topics/test/handlers"},
			},
		},
	}
	topics, err := cli.ListTopics(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(topics, expTopics) {
		t.Errorf("unexpected topics:\ngot\n%+v\nexp\n%+v\n", topics, expTopics)
	}

	tick := `
stream
	|from()
		.measurement('alert')
	|alert()
		.topic('test')
		.id('id')
		.message('message')
		.details('details')
		.crit(lambda: TRUE)
`

	if _, err := cli.CreateTask(client.CreateTaskOptions{
		ID:   "testAlertHandlers",
		Type: client.StreamTask,
		DBRPs: []client.DBRP{{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		}},
		TICKscript: tick,
		Status:     client.Enabled,
	}); err != nil {
		t.Fatal(err)
	}

	point := "alert value=1 0000000000"
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", point, v)

	// Restart the server
	s.Restart()

	// Update expected topics since we triggered an event.
	expTopics.Topics[2].Level = "CRITICAL"

	// Check again
	topics, err = cli.ListTopics(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(topics, expTopics) {
		t.Errorf("unexpected topics after restart:\ngot\n%+v\nexp\n%+v\n", topics, expTopics)
	}

	var exp client.Topics

	// Pattern = *
	topics, err = cli.ListTopics(&client.ListTopicsOptions{
		Pattern: "*",
	})
	if err != nil {
		t.Fatal(err)
	}
	exp = expTopics
	exp.Link.Href = "/kapacitor/v1preview/alerts/topics?min-level=OK&pattern=%2A"
	if !reflect.DeepEqual(topics, exp) {
		t.Errorf("unexpected topics with pattern \"*\":\ngot\n%+v\nexp\n%+v\n", topics, exp)
	}

	// Pattern = test
	topics, err = cli.ListTopics(&client.ListTopicsOptions{
		Pattern: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	exp = expTopics
	exp.Link.Href = "/kapacitor/v1preview/alerts/topics?min-level=OK&pattern=test"
	exp.Topics = expTopics.Topics[2:]
	if !reflect.DeepEqual(topics, exp) {
		t.Errorf("unexpected topics with pattern \"test\":\ngot\n%+v\nexp\n%+v\n", topics, exp)
	}

	// MinLevel = INFO
	topics, err = cli.ListTopics(&client.ListTopicsOptions{
		MinLevel: "INFO",
	})
	if err != nil {
		t.Fatal(err)
	}
	exp = expTopics
	exp.Link.Href = "/kapacitor/v1preview/alerts/topics?min-level=INFO&pattern="
	exp.Topics = expTopics.Topics[2:]
	if !reflect.DeepEqual(topics, exp) {
		t.Errorf("unexpected topics min level \"info\":\ngot\n%+v\nexp\n%+v\n", topics, exp)
	}
}

func TestServer_AlertHandler_MultipleActions(t *testing.T) {
	resultJSON := `{"Series":[{"name":"alert","columns":["time","value"],"values":[["1970-01-01T00:00:00Z",1]]}],"Messages":null,"Err":null}`

	// Create default config
	c := NewConfig()

	// Configure slack
	slack := slacktest.NewServer()
	c.Slack.Enabled = true
	c.Slack.URL = slack.URL + "/test/slack/url"

	// Configure victorops
	vo := victoropstest.NewServer()
	c.VictorOps.Enabled = true
	c.VictorOps.URL = vo.URL
	c.VictorOps.APIKey = "api_key"

	s := OpenServer(c)
	cli := Client(s)
	closed := false
	defer func() {
		if !closed {
			s.Close()
		}
	}()

	if _, err := cli.CreateHandler(client.HandlerOptions{
		ID:     "testAlertHandlers",
		Topics: []string{"test"},
		Actions: []client.HandlerAction{
			{
				Kind: "victorops",
				Options: map[string]interface{}{
					"routing-key": "key",
				},
			},
			{
				Kind: "slack",
				Options: map[string]interface{}{
					"channel": "#test",
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	tick := `
stream
	|from()
		.measurement('alert')
	|alert()
		.topic('test')
		.id('id')
		.message('message')
		.details('details')
		.crit(lambda: TRUE)
`

	if _, err := cli.CreateTask(client.CreateTaskOptions{
		ID:   "testAlertHandlers",
		Type: client.StreamTask,
		DBRPs: []client.DBRP{{
			Database:        "mydb",
			RetentionPolicy: "myrp",
		}},
		TICKscript: tick,
		Status:     client.Enabled,
	}); err != nil {
		t.Fatal(err)
	}

	point := "alert value=1 0000000000"
	v := url.Values{}
	v.Add("precision", "s")
	s.MustWrite("mydb", "myrp", point, v)

	// Close the entire server to ensure all data is processed
	s.Close()
	closed = true

	// Validate slack
	{
		slack.Close()
		got := slack.Requests()
		exp := []slacktest.Request{{
			URL: "/test/slack/url",
			PostData: slacktest.PostData{
				Channel:  "#test",
				Username: "kapacitor",
				Text:     "",
				Attachments: []slacktest.Attachment{
					{
						Fallback:  "message",
						Color:     "danger",
						Text:      "message",
						Mrkdwn_in: []string{"text"},
					},
				},
			},
		}}
		if !reflect.DeepEqual(exp, got) {
			t.Errorf("unexpected slack request:\nexp\n%+v\ngot\n%+v\n", exp, got)
		}
	}
	// Validate victorops
	{
		vo.Close()
		got := vo.Requests()
		exp := []victoropstest.Request{{
			URL: "/api_key/key",
			PostData: victoropstest.PostData{
				MessageType:    "CRITICAL",
				EntityID:       "id",
				StateMessage:   "message",
				Timestamp:      0,
				MonitoringTool: "kapacitor",
				Data:           resultJSON,
			},
		}}
		if !reflect.DeepEqual(exp, got) {
			t.Errorf("unexpected victorops request:\nexp\n%+v\ngot\n%+v\n", exp, got)
		}
	}
}
