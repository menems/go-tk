package config_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/menems/go-tk/config"
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

func TestIntBetween(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  map[string]string
		want    int
		wantErr bool
	}{
		{name: "inside", values: map[string]string{"SLOTS": "8"}, want: 8},
		{name: "lower bound included", values: map[string]string{"SLOTS": "1"}, want: 1},
		{name: "upper bound included", values: map[string]string{"SLOTS": "64"}, want: 64},
		{name: "unset falls back", values: nil, want: 4},
		{name: "empty falls back", values: map[string]string{"SLOTS": ""}, want: 4},
		{name: "below is a problem", values: map[string]string{"SLOTS": "0"}, want: 4, wantErr: true},
		{name: "above is a problem", values: map[string]string{"SLOTS": "65"}, want: 4, wantErr: true},
		{name: "unparseable is a problem", values: map[string]string{"SLOTS": "many"}, want: 4, wantErr: true},
		{name: "beyond an int is a problem", values: map[string]string{"SLOTS": "99999999999999999999"}, want: 4, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(tt.values))
			got := e.IntBetween("SLOTS", 1, 64, 4)

			if (e.Err() != nil) != tt.wantErr {
				t.Fatalf("Err = %v, wantErr %v", e.Err(), tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("IntBetween = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestIntBetweenProblemNamesKeyAndBounds pins what an operator reads: the key,
// the range, and nothing of the value, which may be a credential set under the
// wrong key.
func TestIntBetweenProblemNamesKeyAndBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "below", value: "-7"},
		{name: "above", value: "4097"},
		{name: "unparseable", value: "postgres://user:s3cret@host/db"},
		{name: "beyond an int", value: "123456789012345678901234"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(map[string]string{"SLOTS": tt.value}))
			e.IntBetween("SLOTS", 1, 4096, 4)

			err := e.Err()
			if err == nil {
				t.Fatal("Err = nil, want a problem")
			}
			if got, want := err.Error(), "config: SLOTS is not an integer from 1 to 4096"; got != want {
				t.Errorf("Err = %q, want %q", got, want)
			}
			if strings.Contains(err.Error(), tt.value) {
				t.Errorf("Err = %q, want the value left out", err)
			}
		})
	}
}

// TestIntBetweenWrongBoundsBlameTheCode pins a defect of the caller, not of the
// operator: bounds that admit no value, or a fallback outside them, are a
// problem whatever the environment holds, and the call returns the lower
// bound, never the environment's value nor the fallback.
func TestIntBetweenWrongBoundsBlameTheCode(t *testing.T) {
	t.Parallel()

	const reversed = "config: SLOTS has bounds 64 to 1 in the code, which admit no value"

	tests := []struct {
		name             string
		lo, hi, fallback int
		values           map[string]string
		want             int
		wantErr          string
	}{
		{name: "reversed, unset", lo: 64, hi: 1, fallback: 4, values: nil, want: 64, wantErr: reversed},
		{name: "reversed, set", lo: 64, hi: 1, fallback: 4, values: map[string]string{"SLOTS": "8"}, want: 64, wantErr: reversed},
		{name: "reversed, unparseable", lo: 64, hi: 1, fallback: 4, values: map[string]string{"SLOTS": "many"}, want: 64, wantErr: reversed},
		{name: "fallback below, unset", lo: 1, hi: 64, fallback: 0, values: nil, want: 1,
			wantErr: "config: SLOTS has fallback 0 in the code, outside its bounds 1 to 64"},
		{name: "fallback above, valid", lo: 1, hi: 64, fallback: 100, values: map[string]string{"SLOTS": "8"}, want: 1,
			wantErr: "config: SLOTS has fallback 100 in the code, outside its bounds 1 to 64"},
		{name: "fallback below, set outside", lo: 1, hi: 64, fallback: 0, values: map[string]string{"SLOTS": "-7"}, want: 1,
			wantErr: "config: SLOTS has fallback 0 in the code, outside its bounds 1 to 64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(tt.values))
			got := e.IntBetween("SLOTS", tt.lo, tt.hi, tt.fallback)

			err := e.Err()
			if err == nil {
				t.Fatal("Err = nil, want a problem")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("Err = %q, want %q", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("IntBetween = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIntBetweenWrongBoundsAtEveryCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		lo, hi, fallback int
		problem          string
	}{
		{name: "reversed", lo: 64, hi: 1, fallback: 4, problem: "admit no value"},
		{name: "fallback outside", lo: 1, hi: 64, fallback: 100, problem: "outside its bounds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(nil))
			e.IntBetween("SLOTS", tt.lo, tt.hi, tt.fallback)
			e.IntBetween("SLOTS", tt.lo, tt.hi, tt.fallback)

			err := e.Err()
			if err == nil {
				t.Fatal("Err = nil, want two problems")
			}
			if got := strings.Count(err.Error(), tt.problem); got != 2 {
				t.Errorf("Err = %q holds %d problems, want 2", err, got)
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

func TestList(t *testing.T) {
	t.Parallel()

	fallback := []string{"https://example.test"}

	tests := []struct {
		name    string
		values  map[string]string
		want    []string
		wantErr bool
	}{
		{name: "in order", values: map[string]string{"ORIGINS": "a,b,c"}, want: []string{"a", "b", "c"}},
		{name: "one element", values: map[string]string{"ORIGINS": "a"}, want: []string{"a"}},
		{name: "spaces trimmed", values: map[string]string{"ORIGINS": " a , b "}, want: []string{"a", "b"}},
		{name: "unset falls back", values: nil, want: fallback},
		{name: "empty falls back", values: map[string]string{"ORIGINS": ""}, want: fallback},
		{name: "only spaces is a problem", values: map[string]string{"ORIGINS": "   "}, want: fallback, wantErr: true},
		{name: "empty element is a problem", values: map[string]string{"ORIGINS": "a,,b"}, want: fallback, wantErr: true},
		{name: "trailing comma is a problem", values: map[string]string{"ORIGINS": "a,b,"}, want: fallback, wantErr: true},
		{name: "leading comma is a problem", values: map[string]string{"ORIGINS": ",a"}, want: fallback, wantErr: true},
		{name: "blank element is a problem", values: map[string]string{"ORIGINS": " , "}, want: fallback, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(tt.values))
			got := e.List("ORIGINS", fallback)

			if (e.Err() != nil) != tt.wantErr {
				t.Fatalf("Err = %v, wantErr %v", e.Err(), tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("List = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestListProblemNamesKeyOnly pins what an operator reads: one problem, the
// key, and no element, which may be one API key among several.
func TestListProblemNamesKeyOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		element string
	}{
		{name: "only spaces", value: "   ", element: "   "},
		{name: "empty element", value: "sk-live-4f9a,,sk-live-7b2c", element: "sk-live-4f9a"},
		{name: "trailing comma", value: "sk-live-4f9a,", element: "sk-live-4f9a"},
		{name: "blank element", value: "sk-live-4f9a, ,sk-live-7b2c", element: "sk-live-7b2c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			e := config.New(env(map[string]string{"API_KEYS": tt.value}))
			e.List("API_KEYS", nil)

			err := e.Err()
			if err == nil {
				t.Fatal("Err = nil, want a problem")
			}
			if got, want := err.Error(), "config: API_KEYS is not a comma-separated list without an empty element"; got != want {
				t.Errorf("Err = %q, want %q", got, want)
			}
			if strings.Contains(err.Error(), tt.element) {
				t.Errorf("Err = %q, want the elements left out", err)
			}
		})
	}
}

// TestErrReportsEveryProblemAcrossReaders pins that a bounded integer and a
// list join the other problems in the one error a boot reads.
func TestErrReportsEveryProblemAcrossReaders(t *testing.T) {
	t.Parallel()

	e := config.New(env(map[string]string{"SLOTS": "many", "ORIGINS": "a,,b"}))
	e.IntBetween("SLOTS", 1, 64, 4)
	e.List("ORIGINS", nil)
	e.Required("DSN")

	err := e.Err()
	if err == nil {
		t.Fatal("Err = nil, want three problems")
	}
	for _, key := range []string{"SLOTS", "ORIGINS", "DSN"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Err = %q, want it to name %s", err, key)
		}
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
