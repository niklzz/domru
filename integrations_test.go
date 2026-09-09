package main

import "testing"

func TestChooseIntercom(t *testing.T) {
	one := intercom{place: 1, control: 2, name: "door"}
	if pick, problem, _ := chooseIntercom([]intercom{one}); problem != "" || pick != one {
		t.Fatalf("single intercom must be picked: %+v %q", pick, problem)
	}
	if _, problem, retry := chooseIntercom(nil); problem == "" || !retry {
		t.Fatalf("no intercoms must keep retrying: %q %v", problem, retry)
	}
	if _, problem, retry := chooseIntercom([]intercom{one, {place: 1, control: 3}}); problem == "" || retry {
		t.Fatalf("several intercoms need explicit config, no retry: %q %v", problem, retry)
	}
}
