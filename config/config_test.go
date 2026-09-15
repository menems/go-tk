package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/menems/got-tk/config"
)

// env turns a map into the getenv New takes, so no test touches the process
// environment.
func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestRequired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  map[string]string
		want    string
		wantErr bool
	}{
		{name: "set", values: map[string]string{"DSN": "postgres://x"}, want: "postgres://x"},
		{name: "unset", values: nil, wantErr: true},
		{name: "empty", values: map[string]string{"DSN": ""}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(tt.values))
			got := e.Required("DSN")

			if (e.Err() != nil) != tt.wantErr {
				t.Fatalf("Err = %v, wantErr %v", e.Err(), tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Required = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{name: "set", values: map[string]string{"ADDR": ":9090"}, want: ":9090"},
		{name: "unset falls back", values: nil, want: ":8080"},
		{name: "empty falls back", values: map[string]string{"ADDR": ""}, want: ":8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(tt.values))
			if got := e.String("ADDR", ":8080"); got != tt.want {
				t.Errorf("String = %q, want %q", got, tt.want)
			}
			if err := e.Err(); err != nil {
				t.Errorf("Err = %v, want nil", err)
			}
		})
	}
}

func TestInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  map[string]string
		want    int
		wantErr bool
	}{
		{name: "set", values: map[string]string{"MAX": "25"}, want: 25},
		{name: "negative", values: map[string]string{"MAX": "-1"}, want: -1},
		{name: "unset falls back", values: nil, want: 10},
		{name: "unparseable is a problem", values: map[string]string{"MAX": "many"}, want: 10, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(tt.values))
			got := e.Int("MAX", 10)

			if (e.Err() != nil) != tt.wantErr {
				t.Fatalf("Err = %v, wantErr %v", e.Err(), tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Int = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  map[string]string
		want    time.Duration
		wantErr bool
	}{
		{name: "set", values: map[string]string{"TIMEOUT": "250ms"}, want: 250 * time.Millisecond},
		{name: "unset falls back", values: nil, want: 5 * time.Second},
		{name: "unparseable is a problem", values: map[string]string{"TIMEOUT": "30"}, want: 5 * time.Second, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(tt.values))
			got := e.Duration("TIMEOUT", 5*time.Second)

			if (e.Err() != nil) != tt.wantErr {
				t.Fatalf("Err = %v, wantErr %v", e.Err(), tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Duration = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestErrReportsEveryProblem is the point of the package: an operator fixing a
// deployment sees all three missing values at once, not one per deploy.
func TestErrReportsEveryProblem(t *testing.T) {
	t.Parallel()

	e := config.New(env(map[string]string{"MAX": "many", "TIMEOUT": "30"}))
	e.Required("DSN")
	e.Int("MAX", 10)
	e.Duration("TIMEOUT", time.Second)

	err := e.Err()
	if err == nil {
		t.Fatal("Err = nil, want three problems")
	}
	for _, key := range []string{"DSN", "MAX", "TIMEOUT"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Err = %q, want it to name %s", err, key)
		}
	}
}

// TestErrKeepsValuesOut pins the redaction: a DSN carries a password, and a
// boot error is logged.
func TestErrKeepsValuesOut(t *testing.T) {
	t.Parallel()

	e := config.New(env(map[string]string{"MAX": "postgres://user:s3cret@host/db"}))
	e.Int("MAX", 10)

	if err := e.Err(); err == nil {
		t.Fatal("Err = nil, want a problem")
	} else if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("Err = %q, want the value left out", err)
	}
}

func TestErrNilWhenClean(t *testing.T) {
	t.Parallel()

	e := config.New(env(map[string]string{"DSN": "postgres://x"}))
	e.Required("DSN")
	e.String("ADDR", ":8080")

	if err := e.Err(); err != nil {
		t.Fatalf("Err = %v, want nil", err)
	}
}
