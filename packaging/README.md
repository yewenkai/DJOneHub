# DJOneHub for macOS（Apple Silicon）

适用于搭载 Apple M 系列芯片的 Mac，以及大疆一代 4G 模块（原始 USB `2ca3:4006`，或已转换为移远身份的 `2c7c:0125`）。

## 安装（推荐）

1. 将下载的 ZIP 完整解压，不要只单独拖出其中某个文件。
2. 打开“终端”，输入 `cd `（后面留一个空格），把解压得到的文件夹拖入终端，然后按回车。
3. 执行一次安装命令：

   ```sh
   ./install
   ```

   安装过程可能要求输入 Mac 管理员密码。输入密码时终端不会显示圆点或星号，这是正常现象。

安装完成后，可以在终端的任意目录执行：

```sh
djonehub start
```

安装脚本只在写入 `/usr/local` 时调用 `sudo`，请直接执行 `./install`，不要执行
`sudo ./install`。安装时还会为当前登录用户启动原生来电、未接来电和短信通知助手。
它是带 Dock 图标和状态窗口的前台应用，登录自启时不强制弹窗；点击 Dock 图标即可
打开。顶部菜单栏会常驻“信号格 + 4G”图标，点开可查看实时网速、本次流量、默认
出口、信号/频段和最近短信正文。通知助手只请求本机 DJOneHub 后端，不直接访问硬件。

程序会自动打开 `http://127.0.0.1:7575`。启动程序的终端需要保持打开；按 `Control+C` 即可停止。

也可以在另一个终端的任意目录执行：

```sh
djonehub stop
```

## 免安装使用

如果不想安装，也可以一直保留解压后的文件夹，在该目录执行：

   ```sh
   ./djonehub start
   ```

## 常用命令

```sh
djonehub status       # 查看状态
djonehub logs         # 查看实时日志
djonehub open         # 重新打开管理网页
djonehub start --demo # 不连接硬件，打开演示界面
```

## macOS 阻止打开时

本软件目前没有 Apple Developer ID 签名。首次启动如果被 macOS 阻止，请打开“系统设置 → 隐私与安全性”，确认仍要打开。

如果系统仍提示文件损坏，可在当前目录执行：

```sh
xattr -dr com.apple.quarantine ./djonehub ./bin ./lib
./djonehub start
```

请只对从可信发布页面下载并核对过 SHA-256 的文件执行该命令。

## 日志

程序日志保存在：

```text
~/Library/Logs/DJOneHub/djonehub.log
```

终端只显示启动、停止和错误摘要，不会持续刷出底层 USB 日志。

模块启动或重新插入后，程序会异步检查 `Baiwang` 网络服务的 DHCP；已有有效 IPv4
地址时不会修改配置，仅在没有地址时尝试续租。结果显示在网页“网络诊断”中。

## 当前限制

- 支持 macOS 13 Ventura 至 macOS 26 Tahoe；当前发行包仅支持 Apple Silicon，不支持 Intel Mac。
- 仅监听本机地址，局域网中的其他设备无法直接访问管理网页。
- 本项目为非官方工具，与 DJI、Quectel 及运营商无隶属或授权关系。
- 使用短信和蜂窝数据前，请确认运营商资费、漫游规则及当地法律要求。
