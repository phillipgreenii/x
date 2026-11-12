package unbounded

import "github.com/phillipgreenii/x/cmdrun"

var Bad = cmdrun.Call{Name: "ls"}

// cmdrun:unbounded with no leading marker form is not an annotation here.
var AlsoBad = cmdrun.Call{Name: "ls", Args: []string{"-l"}}
