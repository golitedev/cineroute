package flacfix

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestCleanDescription(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Plain text stays exactly as it is.", "Plain text stays exactly as it is."},
		{
			"Her debut, <em>Sour</em>, felt like a diary. <br /><br />Addictive \"drop dead\" is great.<br />Next line.",
			"Her debut, Sour, felt like a diary.\n\nAddictive \"drop dead\" is great.\nNext line.",
		},
		{"settling down to sleep alone. <em>© </em>Shelly Ridenour/Qobuz", "settling down to sleep alone. © Shelly Ridenour/Qobuz"},
		{"<p>One</p><p>Two &amp; three&nbsp;&quot;four&quot;</p>", "One\n\nTwo & three \"four\""},
		{"Rock &amp; Roll <b>BOLD</b> <i>it</i> <a href=\"x\">link</a>", "Rock & Roll BOLD it link"},
		{"Love you <3 and 2 < 3", "Love you <3 and 2 < 3"},
		{"a<br><br><br><br>b", "a\n\nb"},
		{"<BR/>Upper case tags<BR />work", "Upper case tags\nwork"},
	}
	for _, c := range cases {
		if got := CleanDescription(c.in); got != c.want {
			t.Errorf("CleanDescription(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestDirCleansDescriptions(t *testing.T) {
	need(t, "flac", "metaflac")
	dir := t.TempDir()
	a := encode(t, filepath.Join(dir, "a"), 16, 2, 44100, 1, "TITLE=One")
	if b, err := exec.Command("metaflac", "--set-tag=DESCRIPTION=Her debut, <em>Sour</em>.<br /><br />Second &amp; last.", a).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	frames := audioFrames(t, a)
	rep, err := Dir(context.Background(), dir, Options{CleanDescriptions: true, Confirm: true, FlacBin: "flac", MetaflacBin: "metaflac"})
	if err != nil || rep.Descriptions() != 1 {
		t.Fatalf("descriptions=%d err=%v %+v", rep.Descriptions(), err, rep.Changes)
	}
	got, _ := ReadTag(a, "DESCRIPTION")
	if !slices.Equal(got, []string{"Her debut, Sour.\n\nSecond & last."}) {
		t.Fatalf("description %q", got)
	}
	if string(audioFrames(t, a)) != string(frames) {
		t.Fatal("audio changed")
	}
	if title, _ := ReadTag(a, "TITLE"); !slices.Equal(title, []string{"One"}) {
		t.Fatalf("title %q", title)
	}
	// Running again changes nothing.
	rep, _ = Dir(context.Background(), dir, Options{CleanDescriptions: true, FlacBin: "flac", MetaflacBin: "metaflac"})
	if rep.Descriptions() != 0 {
		t.Fatal("second run changed the description again")
	}
}
