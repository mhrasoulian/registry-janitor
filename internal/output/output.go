package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"
)

// Entry is a single tag decision rendered for human or machine consumption.
type Entry struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest"`
	Created    string `json:"created"`
	Age        string `json:"age"`
	Action     string `json:"action"`
	Reason     string `json:"reason"`
}

func Table(entries []Entry) string {
	stable := append([]Entry(nil), entries...)
	sort.Slice(stable, func(i, j int) bool {
		if stable[i].Repository != stable[j].Repository {
			return stable[i].Repository < stable[j].Repository
		}
		if stable[i].Tag != stable[j].Tag {
			return stable[i].Tag < stable[j].Tag
		}
		return stable[i].Digest < stable[j].Digest
	})
	if len(stable) == 0 {
		return "REPOSITORY\tTAG\tDIGEST\tCREATED\tAGE\tACTION\tREASON"
	}
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY\tTAG\tDIGEST\tCREATED\tAGE\tACTION\tREASON")
	for _, entry := range stable {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			entry.Repository,
			entry.Tag,
			tableDigest(entry.Digest),
			entry.Created,
			entry.Age,
			entry.Action,
			entry.Reason,
		)
	}
	_ = w.Flush()
	return buf.String()
}

func tableDigest(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

func JSON(entries []Entry) ([]byte, error) {
	stable := append([]Entry(nil), entries...)
	sort.Slice(stable, func(i, j int) bool {
		if stable[i].Repository != stable[j].Repository {
			return stable[i].Repository < stable[j].Repository
		}
		if stable[i].Tag != stable[j].Tag {
			return stable[i].Tag < stable[j].Tag
		}
		return stable[i].Digest < stable[j].Digest
	})
	return json.MarshalIndent(stable, "", "  ")
}
