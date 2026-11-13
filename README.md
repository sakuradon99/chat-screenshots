# 叠图 · 聊天截图工作台

## 在这台 Windows 电脑上启动

需要 Go 1.25。项目只用 Go 标准库，前端随服务一起打包。在项目目录的 PowerShell 里运行：

    go run .

保持终端运行。电脑打开 http://127.0.0.1:8080 。手机连接同一个局域网，在浏览器输入电脑页面「手机访问」里的局域网地址。如果 Windows 防火墙询问是否允许访问，选择专用网络。

也可以先编译，再运行：

    go build -o chat-screenshots.exe .
    .\chat-screenshots.exe

可以用参数更改监听地址、端口及数据位置：

    .\chat-screenshots.exe -addr 0.0.0.0:8081 -data .\data

默认监听 8080 端口。默认的 data 目录存放截图组和图片。备份时停止服务并复制整个目录。请用同一个服务实例访问同一数据目录。

## Docker 部署

在项目目录创建 `data` 文件夹后执行 `docker compose up -d --build`。服务通过主机的 `8100` 端口访问。截图和组信息保存在主机的 `./data`，容器以 `DATA_UID:DATA_GID` 运行（默认 `1000:1000`），该用户需要对目录有写权限。

可以在服务器的项目目录创建 `.env`，通过 `BIND_IP` 指定 Ubuntu 要监听的网卡地址。将 `CHAT_SCREENSHOTS_ACCESS_URL` 设为用户访问的完整 URL 后，页面的「手机访问」会显示这个地址。

启用 OAuth 时，在 Authelia 中注册要求双因素和 S256 PKCE 的 `chat-screenshots` OIDC 客户端，回调 URL 与 `compose.oauth.yaml` 一致。在仅部署账户可读的 `oauth.env` 中设置 `OAUTH2_PROXY_CLIENT_SECRET` 和 `OAUTH2_PROXY_COOKIE_SECRET`，然后运行：

    docker compose -f compose.yaml -f compose.oauth.yaml up -d --build

应用在主机的 `8100` 端口提供可信局域网直连。OAuth2 Proxy 在 `8101` 端口提供域名登录入口。只将 HTTPS 域名入口对外开放。NAS 使用 `/volume1/docker/nginx` 下现有的 Nginx 容器。将 `deploy/nginx.conf.example` 保存为 `/volume1/docker/nginx/conf.d/chat-screenshots.conf`，按需调整域名、证书和上游地址。

    docker exec nginx nginx -t
    docker exec nginx nginx -s reload

## 使用

1. 新建截图组，填写组名和日期。
2. 电脑可拖入文件，手机可从相册或文件选择多张。批量上传默认按文件名自然排序，也可以按文件修改时间、系统返回顺序或手动调整。确认后，图片会按列表顺序追加。
3. 电脑可拖动图片右上角的手柄。在任意设备上，可以点击图片序号输入目标位置，或用箭头前移、后移。大图预览支持左右方向键和屏幕按钮切换。组标题旁的「删除组」可删除整个组及其图片。
4. 同组图片的原始尺寸一致时，可点击「批量裁切」。在第一张图上拖出要保留的区域，整组会使用相同的裁切范围。可以随时恢复原图。上传尺寸不同的图片会取消当前裁切。
5. 输入导出宽度，点击「导出长图」。图片按当前顺序和裁切范围等比例缩放并纵向拼接为一张 PNG，透明像素以白底显示。

每组最多 300 张，单张最多 32 MB、2400 万像素。导出宽度为 1–4096 像素，长图最多 5 亿像素。PNG、JPEG 和 GIF 可上传，GIF 长图使用首帧。WebP、HEIC 等格式可在浏览器能够解码时转换后上传。

请在可信局域网内使用。连接到此服务的设备可以查看和编辑截图组。
