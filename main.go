package main

import (
	"bytes"
	"flag"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/golang/protobuf/proto"

	build "github.com/DarkDNA/bazel-gopath/bazel_query_proto"
)

var (
	workspacePath = flag.String("workspace", "", "Location of the Bazel workspace.")
	gopathOut     = flag.String("out-gopath", "", "Defaults to <workspace-path>/.gopath")
)

func main() {
	flag.Parse()

	if *workspacePath == "" {
		log.Fatal("Requires at least -workspace")
	}

	if *gopathOut == "" {
		*gopathOut = filepath.Join(*workspacePath, ".gopath")
	}

	buff := bytes.NewBuffer(nil)

	cmd := exec.Command("bazel", "query", "--output=proto", "-k", `deps(kind("_?go_.* rule", //...))`)
	cmd.Stderr = os.Stderr
	cmd.Stdout = buff
	cmd.Dir = *workspacePath

	if err := cmd.Run(); err != nil {
		log.Printf("cmd.Run returned: %s", err)
	}
 
	var queryResult build.QueryResult
	if err := proto.Unmarshal(buff.Bytes(), &queryResult); err != nil {
		log.Fatal(err)
	}

	processProto(queryResult)
}

func processProto(queryResult build.QueryResult) {
	genOutputs := make(map[string][]string)
	goPrefixes := make(map[string]string)

	for _, target := range queryResult.Target {
		if target.Rule == nil {
			continue
		}

		// outputs[*target.Rule.Name] = nil
		if *target.Rule.RuleClass == "genrule" {
			for _, output := range target.Rule.RuleOutput {
				if strings.HasSuffix(output, ".go") {
					genOutputs[*target.Rule.Name] = append(genOutputs[*target.Rule.Name], output)
				}
			}
		}

		if *target.Rule.RuleClass == "_go_prefix_rule" {
			for _, attr := range target.Rule.Attribute {
				if *attr.Name == "prefix" {
					goPrefixes[*target.Rule.Name] = *attr.StringValue
				}
			}
		}
	}

	log.Printf("Discovered following prefixes: ")
	for lbl, pfx := range goPrefixes {
		log.Printf("%q -> %q", lbl, pfx)
	}

	for _, target := range queryResult.Target {
		if target.Rule == nil {
			continue
		}

		rule := target.Rule
		if rule.RuleClass != nil && *rule.RuleClass != "go_library" && *rule.RuleClass != "go_binary" {
			continue
		}

		ruleWorkspace, ruleLabel, ruleName := parseLabel(*rule.Name)

		var goPrefix string
		for _, attr := range rule.Attribute {
			if *attr.Name == "go_prefix" {
				goPrefix = goPrefixes[ruleWorkspace+*attr.StringValue]
			}
		}

		if goPrefix == "" {
			log.Printf("Failed to discover goPrefix for %q", *rule.Name)
			continue
		}

		if ruleName == "go_default_library" {
			ruleName = ""
		}

		for _, attr := range rule.Attribute {
			if *attr.Name == "srcs" {
				for _, label := range attr.StringListValue {
					workspace, lbl, name := parseLabel(label)

					wsPath := *workspacePath
					if workspace != "" {
						wsPath = filepath.Join(*workspacePath, "bazel-__main__/external/", workspace[1:])
					}

					if outs, ok := genOutputs[label]; ok {
						for _, label := range outs {
							_, lbl, name := parseLabel(label)

							path := filepath.Join(lbl, name)
							pkgPath := filepath.Join(goPrefix, ruleLabel, ruleName, name)

							src := filepath.Join(*workspacePath, "bazel-genfiles", lbl, path)
							dest := filepath.Join(*gopathOut, "src", pkgPath)

							if err := recursiveMkdir(filepath.Dir(dest), os.FileMode(0777)); err != nil && !os.IsExist(err) {
								log.Fatalf("Failed to write make parent directories: %s", err)
							}

							err := os.Symlink(src, dest)
							if err != nil && !os.IsExist(err) {
								log.Fatalf("Failed to symlink %q -> %q: %s", src, dest, err)
							}
						}
					} else if strings.HasSuffix(name, ".go") {
						path := filepath.Join(lbl, name)
						pkgPath := filepath.Join(goPrefix, ruleLabel, ruleName, name)

						src := filepath.Join(wsPath, path)
						dest := filepath.Join(*gopathOut, "src", pkgPath)

						if err := recursiveMkdir(filepath.Dir(dest), os.FileMode(0777)); err != nil && !os.IsExist(err) {
							log.Fatalf("Failed to write make parent directories: %s", err)
						}

						err := os.Symlink(src, dest)
						if err != nil && !os.IsExist(err) {
							log.Fatalf("Failed to symlink %q -> %q: %s", src, dest, err)
						}
					}
				}
			}
		}
	}
}

func parseLabel(inp string) (workspace string, label string, name string) {
	tmp := strings.SplitN(inp, "//", 2)
	workspace = tmp[0]

	tmp = strings.SplitN(tmp[1], ":", 2)
	label, name = tmp[0], tmp[1]

	return workspace, label, name
}

func recursiveMkdir(path string, mode os.FileMode) error {
	if path != "/" {
		if err := recursiveMkdir(filepath.Dir(path), mode); err != nil && !os.IsExist(err) {
			return err
		}
	}

	return os.Mkdir(path, mode)
}