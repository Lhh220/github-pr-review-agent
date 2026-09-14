package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Case struct {
	Name     string
	Fixture  Fixture
	Expected Expected
	Script   []ScriptResponse
}

func LoadCases(root string) ([]Case, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read eval cases: %w", err)
	}

	cases := make([]Case, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		evaluationCase, err := LoadCase(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		cases = append(cases, evaluationCase)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("no eval cases found in %s", root)
	}
	return cases, nil
}

func LoadCase(directory string) (Case, error) {
	var fixture Fixture
	if err := readJSONFile(filepath.Join(directory, "fixture", "case.json"), &fixture); err != nil {
		return Case{}, fmt.Errorf("load fixture %s: %w", filepath.Base(directory), err)
	}

	var expected Expected
	if err := readJSONFile(filepath.Join(directory, "expected.json"), &expected); err != nil {
		return Case{}, fmt.Errorf("load expectations %s: %w", filepath.Base(directory), err)
	}
	if expected.Findings == nil {
		expected.Findings = []ExpectedFinding{}
	}

	var script []ScriptResponse
	if err := readJSONFile(filepath.Join(directory, "fixture", "script.json"), &script); err != nil {
		return Case{}, fmt.Errorf("load script %s: %w", filepath.Base(directory), err)
	}
	return Case{
		Name:     filepath.Base(directory),
		Fixture:  fixture,
		Expected: expected,
		Script:   script,
	}, nil
}

func readJSONFile(path string, out any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
