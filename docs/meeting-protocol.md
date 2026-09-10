# EchoEar 会议音频上传协议（MVP）

会议上传使用独立 WebSocket，不复用 `/xiaozhi/v1/` 的小智对话音频链路：

```text
ws://<server>:8989/xiaozhi/meeting/v1/?device_id=<device-id>
```

也可以使用 `Device-Id` 请求头。当前实现只接受 `pcm_s16le`，服务端边接收边写 WAV，不把整场会议放入内存。

## 生命周期

客户端先发送文本 JSON：

```json
{
  "type": "meeting.start",
  "meeting_id": "20260904-001",
  "audio_params": {
    "format": "pcm_s16le",
    "sample_rate": 16000,
    "channels": 2,
    "frame_duration_ms": 20
  }
}
```

服务端返回 `meeting.ready`。`sample_rate` 当前支持 8000～48000 Hz，`channels` 支持 1 或 2。

ASR 可用时，服务端会在录音过程中发送实时转写：

```json
{
  "type": "meeting.transcript",
  "meeting_id": "20260904-001",
  "text": "正在讨论下一阶段计划",
  "speaker": "张三",
  "is_final": true
}
```

`is_final=false` 表示可被后续结果替换的临时文本；`is_final=true` 表示可写入会议记录的确定分段。已登记且匹配成功的声纹显示姓名，否则显示“未知发言人”。

随后每个 WebSocket 二进制消息都是一个会议帧：

| 偏移 | 大小 | 字段 | 编码 |
| ---: | ---: | --- | --- |
| 0 | 4 | magic | ASCII `EAMF` |
| 4 | 1 | version | `1` |
| 5 | 1 | encoding | `0`，S16LE |
| 6 | 4 | sequence | 无符号 32 位，小端 |
| 10 | 8 | timestamp_ms | 设备单调时钟，64 位，小端 |
| 18 | 4 | payload_bytes | 无符号 32 位，小端 |
| 22 | 2 | reserved | 填 `0` |
| 24 | N | payload | 交错 PCM，16 位小端 |

双声道数据排列为 `L0,R0,L1,R1,...`。每帧必须非空，并且字节数是 `2 * channels` 的整数倍。服务端允许序号跳跃，会在停止结果中报告 `missing_frames`；序号倒退会报错。

结束时发送：

```json
{"type":"meeting.stop"}
```

服务端返回：

```json
{
  "type": "meeting.stop",
  "result": {
    "device_id": "28:0A:C6:1D:3B:E8",
    "meeting_id": "20260904-001",
    "path": "data/meetings/28_0A_C6_1D_3B_E8_20260904-001_20260904T...Z.wav",
    "sample_rate": 16000,
    "channels": 2,
    "frames": 10,
    "bytes": 12800,
    "duration_ms": 203
  }
}
```

连接异常时服务端也会尝试收尾 WAV。未收尾的临时文件使用 `.wav.part` 后缀，运维可以清理或恢复前先检查文件头。

## 当前范围

- 已实现：会议生命周期、帧校验、序号丢失统计、流式 WAV 落盘、复用设备 ASR 的实时/最终转写、已登记声纹识别、会议数据库、AI 纪要。
- 管理后台接口：`GET /api/user/meetings`、`GET /api/user/meetings/:id`。
- 未实现：未登记参会者的 A/B/C 聚类式说话人分离、DOA 元数据上传、会议音频在线播放。
- 固件接入时必须让会议采集链路取 MIC1 与 MIC2（EchoEar 的 TDM slot 0 和 slot 2），不能把现有 AEC 的 MIC1 + MIC3 直接声明为双麦。
- 会议模式建议与普通小智对话互斥；会议数据不要送入现有 `ChatManager`，否则会受到 VAD、`listen` 状态和单声道 ASR 的影响。
