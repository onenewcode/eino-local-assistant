# AGENTS.md

## 编码约束

- 新增或升级依赖时使用 `go get <module>@latest`，不得手动指定语义化版本或直接编辑版本号


## 测试门槛

每次可交付变更至少运行：

```sh
go test ./...
go build ./...
```
