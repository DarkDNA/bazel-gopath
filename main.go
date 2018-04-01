// Copyright (c) 2016 DarkDNA
//
// Permission is hereby granted, free of charge, to any person obtaining a copy of
// this software and associated documentation files (the "Software"), to deal in
// the Software without restriction, including without limitation the rights to
// use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
// the Software, and to permit persons to whom the Software is furnished to do so,
// subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
// FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
// COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
// IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
// CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

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
	bazelPath     = flag.String("bazel-bin", "bazel", "Location of bazel binary")
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

	cmd := exec.Command(*bazelPath, "query", "--output=proto", "-k", "deps(kind('_?go_.*|proto_compile|proto_library rule', //...))")
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

var protoFileMap = map[string]string{
	"pb": ".pb.go",
	"gw": ".pb.gw.go",
}

func processProto(queryResult build.QueryResult) {
	protoSrcs := make(map[string][]string)
	protoGenSuffix := make(map[string]string)
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

		if *target.Rule.RuleClass == "proto_compile" {
			log.Printf("Found proto: %q", *target.Rule.Name)
			tmp := strings.Split(*target.Rule.Name, ".")

			for _, attr := range target.Rule.Attribute {
				if *attr.Name == "protos" {
					for _, val := range attr.StringListValue {
						genOutputs[*target.Rule.Name] = append(genOutputs[*target.Rule.Name],
							strings.Replace(val, ".proto", protoFileMap[tmp[len(tmp)-1]], 1))
					}
				}
			}
		}

		if *target.Rule.RuleClass == "proto_library" {
			for _, attr := range target.Rule.Attribute {
				if *attr.Name == "srcs" {
					protoSrcs[*target.Rule.Name] = attr.StringListValue
				}
			}
		}

		if *target.Rule.RuleClass == "go_proto_compiler" {
			log.Printf("Found proto generator: %q", *target.Rule.Name)

			for _, attr := range target.Rule.Attribute {
				if *attr.Name == "suffix" {
					protoGenSuffix[*target.Rule.Name] = *attr.StringValue
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
		if rule.RuleClass != nil && *rule.RuleClass != "go_library" && *rule.RuleClass != "go_proto_library" {
			continue
		}

		ruleWorkspace, ruleLabel, ruleName := parseLabel(*rule.Name)
		_ = ruleWorkspace

		var goPrefix string
		var legacy bool

		for _, attr := range rule.Attribute {
			if *attr.Name == "importpath" {
				goPrefix = *attr.StringValue
				break
			}
		}

		// Seems go_prefix was made private, grab from the inputs instead.
		if goPrefix == "" {
			for _, inp := range rule.RuleInput {
				if inp[len(inp)-10:] == ":go_prefix" {
					goPrefix = goPrefixes[inp]
					break
				}
			}
		}

		if goPrefix == "" {
			log.Printf("Failed to discover goPrefix for %q", *rule.Name)
			continue
		}

		if ruleName == "go_default_library" {
			ruleName = ""
		}

		if *target.Rule.RuleClass == "go_proto_library" {
			log.Printf("Found proto: %q", *target.Rule.Name)
			var srcs []string
			var generators []string

			for _, attr := range target.Rule.Attribute {
				if *attr.Name == "proto" {
					tmp, ok := protoSrcs[*attr.StringValue]
					if !ok {
						log.Fatal("Invalid go_proto_library: Missing src: %q", *attr.StringValue)
					}

					srcs = tmp
				} else if *attr.Name == "compilers" {
					generators = attr.StringListValue
				}
			}

			for _, tmp := range generators {
				genSuffix, ok := protoGenSuffix[tmp]
				if !ok {
					continue
				}

				for _, label := range srcs {
					_, lbl, name := parseLabel(label)
					name = strings.Replace(name, ".proto", genSuffix, 1)

					pkgPath := filepath.Join(goPrefix, filepath.Base(name))

					src := filepath.Join(*workspacePath, "bazel-genfiles", lbl, name)
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
		} else if *target.Rule.RuleClass == "go_library" {
			for _, attr := range rule.Attribute {
				if *attr.Name == "srcs" {
					for _, label := range attr.StringListValue {
						workspace, lbl, name := parseLabel(label)

						wsPath := *workspacePath
						if workspace != "" {
							wsPath = filepath.Join(*workspacePath, "bazel-"+filepath.Base(*workspacePath)+"/external/", workspace[1:])
						}

						if outs, ok := genOutputs[label]; ok {
							for _, label := range outs {
								_, lbl, name := parseLabel(label)
								var pkgPath string

								if legacy {
									pkgPath = filepath.Join(goPrefix, ruleLabel, ruleName, filepath.Base(name))
								} else {
									pkgPath = filepath.Join(goPrefix, filepath.Base(name))
								}

								src := filepath.Join(*workspacePath, "bazel-genfiles", lbl, name)
								dest := filepath.Join(*gopathOut, "src", pkgPath)

								if err := recursiveMkdir(filepath.Dir(dest), os.FileMode(0777)); err != nil && !os.IsExist(err) {
									log.Fatalf("Failed to write make parent directories: %s", err)
								}

								err := os.Symlink(src, dest)
								if err != nil && !os.IsExist(err) {
									log.Fatalf("Failed to symlink %q -> %q: %s", src, dest, err)
								}
							}
						} else if strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".S") || strings.HasSuffix(name, ".s") || strings.HasSuffix(name, ".h") {
							path := filepath.Join(lbl, name)
							var pkgPath string

							if legacy {
								pkgPath = filepath.Join(goPrefix, ruleLabel, ruleName, filepath.Base(name))
							} else {
								pkgPath = filepath.Join(goPrefix, filepath.Base(name))
							}

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
}

func parseLabel(inp string) (workspace string, label string, name string) {
	tmp := strings.SplitN(inp, "//", 2)
	workspace = tmp[0]

	tmp = strings.SplitN(tmp[1], ":", 2)
	label, name = tmp[0], tmp[1]

	return workspace, label, name
}

func recursiveMkdir(path string, mode os.FileMode) error {
	if path == *workspacePath {
		return nil
	}

	if err := recursiveMkdir(filepath.Dir(path), mode); err != nil && !os.IsExist(err) {
		return err
	}

	return os.Mkdir(path, mode)
}
