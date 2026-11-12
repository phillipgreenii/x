package bounded

import (
	"time"

	"github.com/phillipgreenii/x/cmdrun"
)

var Good = cmdrun.Call{Name: "ls", Timeout: 5 * time.Second}
