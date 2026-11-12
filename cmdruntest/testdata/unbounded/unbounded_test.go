package unbounded

import "github.com/phillipgreenii/x/cmdrun"

// Test files are exempt from the guard.
var InTest = cmdrun.Call{Name: "ls"}
