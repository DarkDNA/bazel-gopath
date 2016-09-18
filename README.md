# bazel-gopath
`bazel-gopath` is a tool to be run against a [Bazel](https://bazel.io) repo  that contains [bazel rules_go targets](https://github.com/bazelbuild/rules_go/).

What it does is runs a query against the bazel workspace using `bazel query` and then creates a go-tool compatable GOPATH directory which symlinks all the bazel-side files into the proper places in the gopath side files, following as much of the bazel go's rules as possible.