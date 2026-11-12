package annotated

import "github.com/phillipgreenii/x/cmdrun"

//cmdrun:unbounded interactive editor runs until the user quits
var Above = cmdrun.Call{Name: "vi"}

var SameLine = cmdrun.Call{Name: "less"} //cmdrun:unbounded pager waits on the user
