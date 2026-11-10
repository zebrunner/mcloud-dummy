package android

import (
	"bufio"
	"os"
	"sort"
	"strings"
	"testing"
)

// The fake UiAutomator2 server has the routes of the real one (tests/contract/uia2-routes-9.10.5.txt, parsed from
// AppiumServlet.java): a route only the fake has would hide a command the real server does not know.
func TestUIA2RoutesAreTheRealOnes(t *testing.T) {
	f, err := os.Open("../../tests/contract/uia2-routes-" + uia2Version + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var real []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) >= 2 {
			real = append(real, fields[0]+" "+fields[1])
		}
	}
	var fake []string
	for _, r := range uia2Routes {
		fake = append(fake, r.method+" "+r.path)
	}
	sort.Strings(real)
	sort.Strings(fake)
	if strings.Join(real, "\n") != strings.Join(fake, "\n") {
		t.Fatalf("the routes differ:\nreal:\n%s\n\nfake:\n%s", strings.Join(real, "\n"), strings.Join(fake, "\n"))
	}
}
