package main

import (
	"testing"

	qt "github.com/frankban/quicktest"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// TestDisableBlindCSPEnvName pins the single environment variable of the anonymous voting switch:
// VOCDONI_DISABLE_BLINDCSP is read, and the underscore-less VOCDONI_DISABLEBLINDCSP is not.
func TestDisableBlindCSPEnvName(t *testing.T) {
	read := func(t *testing.T, env map[string]string) bool {
		t.Helper()
		for name, value := range env {
			t.Setenv(name, value)
		}
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.Bool(disableBlindCSPKey, false, "")
		v := viper.New()
		qt.Assert(t, initViper(v, flags), qt.IsNil)
		return v.GetBool(disableBlindCSPKey)
	}
	t.Run("default", func(t *testing.T) {
		qt.Assert(t, read(t, nil), qt.IsFalse)
	})
	t.Run("documented name", func(t *testing.T) {
		qt.Assert(t, read(t, map[string]string{"VOCDONI_DISABLE_BLINDCSP": "true"}), qt.IsTrue)
	})
	t.Run("name without underscore is ignored", func(t *testing.T) {
		qt.Assert(t, read(t, map[string]string{"VOCDONI_DISABLEBLINDCSP": "true"}), qt.IsFalse)
	})
	t.Run("documented name wins", func(t *testing.T) {
		env := map[string]string{"VOCDONI_DISABLE_BLINDCSP": "true", "VOCDONI_DISABLEBLINDCSP": "false"}
		qt.Assert(t, read(t, env), qt.IsTrue)
	})
}
