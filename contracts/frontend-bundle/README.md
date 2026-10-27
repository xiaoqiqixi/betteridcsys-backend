# 前端静态包协议

`frontend_bundle.py` 与 `LICENSE` 原样来自 [前端固定提交 aa86bf0](https://github.com/xiaoqiqixi/betteridcsys-frontend/tree/aa86bf06eb4de9dd4fc95ff8d902c164de76784f)，来源路径及 SHA256 见 [SOURCE.json](SOURCE.json)。这里没有前端应用源码。

后端组合工具只使用其只读 `inspect` 协议，并在加载前核对工具固定摘要。该协议验证静态文件的结构与内容，不认证 GitHub 发行或签名。升级协议工具需要重新审查和测试，不能随前端可变 main 自动替换。

被测前端的具体版本与源码仍由后端根目录 `FRONTEND_PAIR.json` 固定；协议工具版本不是应用版本。
