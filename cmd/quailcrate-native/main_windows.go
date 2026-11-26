// QuailCrateNative runs one native ABI plugin inside an isolated worker process.
package main

import (
	"os"

	"quailcrate/internal/plugins"
)

func main() {
	if (len(os.Args) != 2 && len(os.Args) != 3) || os.Args[1] != plugins.NativeWorkerArgument {
		os.Exit(2)
	}
	if err := plugins.RunNativeWorker(); err != nil {
		os.Exit(1)
	}
}
