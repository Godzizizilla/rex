import Foundation

// Mirrors of the control protocol types in internal/protocol/protocol.go.
// JSON keys are snake_case and decoded with .convertFromSnakeCase.
// Timestamps are Unix milliseconds.

struct PolicyCommand: Codable, Hashable {
    var raw: String
    var name: String?
    var op: String?
    var nested: Bool?
    var whitelisted: Bool?
    var rule: String?
    var risk: String?
}

struct Evaluation: Codable, Hashable {
    var kind: String
    var target: String?
    var commands: [PolicyCommand]?
    var constructs: [String]?
    var opaque: [String]?
    var riskyWrites: [String]?
    var whitelisted: Bool?
    var rule: String?
    var risks: [String]?
}

struct RexRequest: Codable, Identifiable, Hashable {
    var id: Int
    var kind: String
    var server: String
    var host: String
    var desc: String
    var timeout: Int

    var command: String?
    var stdinSize: Int?
    var stdinPreview: String?

    var localPath: String?
    var remotePath: String?
    var size: Int64?
    var diff: String?

    var localPort: Int?
    var remoteHost: String?
    var remotePort: Int?
    var ttl: Int?

    var eval: Evaluation?

    var status: String
    var approvedBy: String?
    var heldBy: [String]?
    var feedback: String?
    var error: String?
    var output: String?
    var outputSize: Int64?
    var exitCode: Int?

    var createdAt: Int64
    var deadline: Int64?
    var decidedAt: Int64?
    var finishedAt: Int64?

    var isPending: Bool { status == "pending" }
    /// High-risk requests need a deliberate press-and-hold to approve.
    var isRisky: Bool { !(eval?.risks ?? []).isEmpty }
    var risks: [String] { eval?.risks ?? [] }

    /// One-line summary used in lists and notifications.
    var summary: String {
        switch kind {
        case "upload": return "上传 \(URL(fileURLWithPath: localPath ?? "").lastPathComponent) → \(remotePath ?? "")"
        case "download": return "下载 \(remotePath ?? "")"
        case "forward":
            let local = (localPort ?? 0) == 0 ? "自动端口" : "127.0.0.1:\(localPort!)"
            return "端口转发 \(local) → \(remoteHost ?? "localhost"):\(remotePort ?? 0)"
        default: return command ?? ""
        }
    }
}

struct Forward: Codable, Identifiable, Hashable {
    var id: Int
    var requestId: Int
    var server: String
    var desc: String
    var localPort: Int
    var remoteHost: String
    var remotePort: Int
    var createdAt: Int64
    var expiresAt: Int64
}

struct AutoState: Codable, Hashable {
    var active: Bool
    var startedAt: Int64?
    var until: Int64?
    var servers: [String]?
    var approved: Int
    var held: Int
    var stopReason: String?
}

struct DaemonState: Codable, Hashable {
    var auto: AutoState
    var forwards: [Forward]?

    static let empty = DaemonState(auto: AutoState(active: false, approved: 0, held: 0), forwards: [])
}

struct ServerInfo: Codable, Identifiable, Hashable {
    var name: String
    var host: String
    var port: Int
    var description: String?
    var id: String { name }
}

struct WhitelistRule: Codable, Hashable {
    var type: String?
    var pattern: String
    var servers: [String]?
    var note: String?

    var kind: String { (type ?? "").isEmpty ? "exec" : type! }
}

struct ConfigView: Codable, Hashable {
    var path: String
    var servers: [ServerInfo]?
    var timeout: Int
    var whitelist: [WhitelistRule]?
    var blocklist: [String]?
    var autoMaxMinutes: Int
}

struct ControlEvent: Decodable {
    var type: String
    var version: Int?
    var seq: Int?
    var ok: Bool?
    var error: String?
    var message: String?
    var request: RexRequest?
    var requests: [RexRequest]?
    var state: DaemonState?
    var config: ConfigView?
}

struct ControlCmd: Encodable {
    var seq: Int?
    var type: String
    var id: Int?
    var feedback: String?
    var indices: [Int]?
    var minutes: Int?
    var servers: [String]?
    var rule: WhitelistRule?
    var index: Int?
    var limit: Int?
}

struct Notice: Identifiable, Equatable {
    let id = UUID()
    var text: String
    var isError = false
    var date = Date()
}

extension Int64 {
    var date: Date { Date(timeIntervalSince1970: TimeInterval(self) / 1000) }
}

enum Fmt {
    static let time: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "HH:mm"
        return f
    }()

    static let dateTime: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "MM-dd HH:mm:ss"
        return f
    }()

    static func bytes(_ n: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: n, countStyle: .file)
    }

    static func remaining(until: Int64) -> String {
        let s = max(0, Int((until.date.timeIntervalSinceNow).rounded()))
        if s >= 3600 { return "\(s / 3600)h\(String(format: "%02d", (s % 3600) / 60))m" }
        if s >= 60 { return "\(s / 60)m" }
        return "\(s)s"
    }
}

extension RexRequest {
    var statusLabel: String {
        switch status {
        case "pending": return "待审批"
        case "approved": return "已批准"
        case "running": return "执行中"
        case "completed": return (exitCode ?? 0) == 0 ? "完成" : "完成 (exit \(exitCode ?? 0))"
        case "rejected": return "已拒绝"
        case "timeout": return "超时"
        case "cancelled": return "已撤回"
        case "failed": return "失败"
        default: return status
        }
    }

    var approvedByLabel: String? {
        switch approvedBy {
        case "manual": return "人工批准"
        case "whitelist": return "白名单"
        case "auto": return "自动审批"
        default: return nil
        }
    }

    var kindSymbol: String {
        switch kind {
        case "upload": return "arrow.up.doc"
        case "download": return "arrow.down.doc"
        case "forward": return "point.3.connected.trianglepath.dotted"
        default: return "terminal"
        }
    }
}
