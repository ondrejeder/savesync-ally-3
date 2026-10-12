package engine

import (
	"reflect"
	"testing"
)

func l(md5 string) LocalFile { return LocalFile{MD5: md5} }
func c(md5 string) CloudFile { return CloudFile{ID: "id-" + md5, MD5: md5} }

func TestMakePlanSingleFile(t *testing.T) {
	tests := []struct {
		name     string
		base     Baseline
		local    map[string]LocalFile
		cloud    map[string]CloudFile
		want     []Action
		conflict bool
	}{
		{"in sync", Baseline{"s": "a"}, map[string]LocalFile{"s": l("a")}, map[string]CloudFile{"s": c("a")}, nil, false},
		{"local edit uploads", Baseline{"s": "a"}, map[string]LocalFile{"s": l("b")}, map[string]CloudFile{"s": c("a")},
			[]Action{{Kind: Upload, Path: "s", CloudID: "id-a"}}, false},
		{"cloud edit downloads", Baseline{"s": "a"}, map[string]LocalFile{"s": l("a")}, map[string]CloudFile{"s": c("b")},
			[]Action{{Kind: Download, Path: "s", CloudID: "id-b"}}, false},
		{"both edited differently conflicts", Baseline{"s": "a"}, map[string]LocalFile{"s": l("b")}, map[string]CloudFile{"s": c("c")}, nil, true},
		{"both edited to same content accepts", Baseline{"s": "a"}, map[string]LocalFile{"s": l("b")}, map[string]CloudFile{"s": c("b")},
			[]Action{{Kind: Accept, Path: "s", CloudID: "id-b"}}, false},
		{"new local file uploads", Baseline{}, map[string]LocalFile{"s": l("a")}, map[string]CloudFile{}, []Action{{Kind: Upload, Path: "s"}}, false},
		{"new cloud file downloads", Baseline{}, map[string]LocalFile{}, map[string]CloudFile{"s": c("a")},
			[]Action{{Kind: Download, Path: "s", CloudID: "id-a"}}, false},
		{"first sync with different content conflicts", Baseline{}, map[string]LocalFile{"s": l("a")}, map[string]CloudFile{"s": c("b")}, nil, true},
		{"first sync with same content accepts", Baseline{}, map[string]LocalFile{"s": l("a")}, map[string]CloudFile{"s": c("a")},
			[]Action{{Kind: Accept, Path: "s", CloudID: "id-a"}}, false},
		{"local delete propagates", Baseline{"s": "a", "k": "k"}, map[string]LocalFile{"k": l("k")}, map[string]CloudFile{"s": c("a"), "k": c("k")},
			[]Action{{Kind: DeleteCloud, Path: "s", CloudID: "id-a"}}, false},
		{"cloud delete propagates", Baseline{"s": "a", "k": "k"}, map[string]LocalFile{"s": l("a"), "k": l("k")}, map[string]CloudFile{"k": c("k")},
			[]Action{{Kind: DeleteLocal, Path: "s"}}, false},
		{"local delete vs cloud edit conflicts", Baseline{"s": "a", "k": "k"}, map[string]LocalFile{"k": l("k")}, map[string]CloudFile{"s": c("b"), "k": c("k")}, nil, true},
		{"deleted on both sides is forgotten", Baseline{"s": "a", "k": "k"}, map[string]LocalFile{"k": l("k")}, map[string]CloudFile{"k": c("k")},
			[]Action{{Kind: Forget, Path: "s"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MakePlan(tt.base, tt.local, tt.cloud)
			if (len(got.Conflicts) > 0) != tt.conflict {
				t.Fatalf("conflicts = %v, want conflict %v", got.Conflicts, tt.conflict)
			}
			if !reflect.DeepEqual(got.Actions, tt.want) {
				t.Fatalf("actions = %+v, want %+v", got.Actions, tt.want)
			}
		})
	}
}

// The scenario that v2 got wrong: the handheld plays offline while the PC
// uploads a newer save. Each device keeps its own baseline, so the handheld
// must see a conflict instead of silently overwriting either side.
func TestTwoDevicesOfflineEditConflicts(t *testing.T) {
	pcBase := Baseline{"save.dat": "v1"}
	allyBase := Baseline{"save.dat": "v1"}
	cloud := map[string]CloudFile{"save.dat": c("v1")}

	// PC plays and syncs.
	pc := MakePlan(pcBase, map[string]LocalFile{"save.dat": l("pc2")}, cloud)
	if len(pc.Conflicts) != 0 || len(pc.Actions) != 1 || pc.Actions[0].Kind != Upload {
		t.Fatalf("pc plan = %+v", pc)
	}
	cloud = map[string]CloudFile{"save.dat": c("pc2")}

	// Ally played offline and now syncs.
	ally := MakePlan(allyBase, map[string]LocalFile{"save.dat": l("ally2")}, cloud)
	if len(ally.Conflicts) != 1 || len(ally.Actions) != 0 {
		t.Fatalf("ally plan = %+v, want conflict", ally)
	}
}

// Normal hand-over: PC uploads, Ally (unchanged since last sync) downloads.
func TestTwoDevicesHandOver(t *testing.T) {
	cloud := map[string]CloudFile{"save.dat": c("pc2")}
	ally := MakePlan(Baseline{"save.dat": "v1"}, map[string]LocalFile{"save.dat": l("v1")}, cloud)
	want := []Action{{Kind: Download, Path: "save.dat", CloudID: "id-pc2"}}
	if len(ally.Conflicts) != 0 || !reflect.DeepEqual(ally.Actions, want) {
		t.Fatalf("ally plan = %+v", ally)
	}
}

func TestConflictPausesWholeGame(t *testing.T) {
	got := MakePlan(
		Baseline{"a": "1", "b": "1"},
		map[string]LocalFile{"a": l("2"), "b": l("x")},
		map[string]CloudFile{"a": c("3"), "b": c("1")},
	)
	if len(got.Conflicts) != 1 || got.Actions != nil {
		t.Fatalf("plan = %+v, want only conflict on a and no actions", got)
	}
}

func TestEmptiedSideIsNeverPropagated(t *testing.T) {
	base := Baseline{"a": "1", "b": "2"}
	wiped := MakePlan(base, map[string]LocalFile{}, map[string]CloudFile{"a": c("1"), "b": c("2")})
	if len(wiped.Conflicts) != 2 || wiped.Actions != nil {
		t.Fatalf("local wiped: plan = %+v", wiped)
	}
	cloudGone := MakePlan(base, map[string]LocalFile{"a": l("1"), "b": l("2")}, map[string]CloudFile{})
	if len(cloudGone.Conflicts) != 2 || cloudGone.Actions != nil {
		t.Fatalf("cloud emptied: plan = %+v", cloudGone)
	}
}

func TestResolvePlan(t *testing.T) {
	local := map[string]LocalFile{"same": l("s"), "diff": l("L"), "onlyLocal": l("o")}
	cloud := map[string]CloudFile{"same": c("s"), "diff": c("C"), "onlyCloud": c("x")}

	keepLocal := ResolvePlan(local, cloud, KeepLocal)
	wantLocal := []Action{
		{Kind: Upload, Path: "diff", CloudID: "id-C"},
		{Kind: DeleteCloud, Path: "onlyCloud", CloudID: "id-x"},
		{Kind: Upload, Path: "onlyLocal"},
		{Kind: Accept, Path: "same", CloudID: "id-s"},
	}
	if !reflect.DeepEqual(keepLocal.Actions, wantLocal) {
		t.Fatalf("keep local = %+v", keepLocal.Actions)
	}

	keepCloud := ResolvePlan(local, cloud, KeepCloud)
	wantCloud := []Action{
		{Kind: Download, Path: "diff", CloudID: "id-C"},
		{Kind: Download, Path: "onlyCloud", CloudID: "id-x"},
		{Kind: DeleteLocal, Path: "onlyLocal"},
		{Kind: Accept, Path: "same", CloudID: "id-s"},
	}
	if !reflect.DeepEqual(keepCloud.Actions, wantCloud) {
		t.Fatalf("keep cloud = %+v", keepCloud.Actions)
	}
}
