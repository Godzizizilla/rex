import Foundation

/// Spawns rexd and speaks the control protocol over its stdin/stdout.
///
/// Only this process holds the pipes, so only Rex.app can approve requests;
/// agents can merely submit them through ~/.rex/rex.sock. When the app quits,
/// rexd sees EOF on stdin and shuts down (closing port forwards).
final class DaemonClient: @unchecked Sendable {
    var onEvent: (@MainActor (ControlEvent) -> Void)?
    var onExit: (@MainActor (String) -> Void)?

    private var process: Process?
    private var input: FileHandle?
    private var buffer = Data()
    private var stderrTail = Data()
    private var log: FileHandle?
    private var seq = 0

    private let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        return d
    }()

    private let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.keyEncodingStrategy = .convertToSnakeCase
        return e
    }()

    var isRunning: Bool { process?.isRunning ?? false }

    static var rexHome: URL {
        if let h = ProcessInfo.processInfo.environment["REX_HOME"], !h.isEmpty {
            return URL(fileURLWithPath: h)
        }
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".rex")
    }

    /// rexd next to the app binary, or $REXD_PATH, or a PATH install.
    static func rexdURL() -> URL? {
        if let p = ProcessInfo.processInfo.environment["REXD_PATH"], !p.isEmpty {
            return URL(fileURLWithPath: p)
        }
        if let u = Bundle.main.url(forAuxiliaryExecutable: "rexd") {
            return u
        }
        for p in ["/usr/local/bin/rexd", "/opt/homebrew/bin/rexd"] where FileManager.default.isExecutableFile(atPath: p) {
            return URL(fileURLWithPath: p)
        }
        return nil
    }

    func start() throws {
        guard let url = Self.rexdURL() else {
            throw NSError(domain: "Rex", code: 1, userInfo: [NSLocalizedDescriptionKey:
                "找不到 rexd，请用 make app 构建，或设置 REXD_PATH"])
        }
        buffer = Data()
        stderrTail = Data()
        try? FileManager.default.createDirectory(at: Self.rexHome, withIntermediateDirectories: true)
        let logURL = Self.rexHome.appendingPathComponent("rexd.log")
        if !FileManager.default.fileExists(atPath: logURL.path) {
            FileManager.default.createFile(atPath: logURL.path, contents: nil)
        }
        log = try? FileHandle(forWritingTo: logURL)
        _ = try? log?.seekToEnd()

        let p = Process()
        p.executableURL = url
        let inPipe = Pipe(), outPipe = Pipe(), errPipe = Pipe()
        p.standardInput = inPipe
        p.standardOutput = outPipe
        p.standardError = errPipe

        outPipe.fileHandleForReading.readabilityHandler = { [weak self] h in
            let data = h.availableData
            if data.isEmpty {
                h.readabilityHandler = nil
                return
            }
            self?.consume(data)
        }
        errPipe.fileHandleForReading.readabilityHandler = { [weak self] h in
            let data = h.availableData
            if data.isEmpty {
                h.readabilityHandler = nil
                return
            }
            self?.logStderr(data)
        }
        p.terminationHandler = { [weak self] _ in
            // Give the stderr handler a moment to flush the last lines.
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                MainActor.assumeIsolated {
                    guard let self else { return }
                    self.onExit?(self.stderrSummary())
                }
            }
        }
        try p.run()
        process = p
        input = inPipe.fileHandleForWriting
    }

    /// Closes rexd's stdin, which makes it shut down gracefully.
    func stop() {
        try? input?.close()
        input = nil
    }

    @discardableResult
    func send(_ cmd: ControlCmd) -> Int {
        var c = cmd
        seq += 1
        c.seq = seq
        guard var data = try? encoder.encode(c) else { return seq }
        data.append(0x0A)
        try? input?.write(contentsOf: data)
        return seq
    }

    private func consume(_ data: Data) {
        buffer.append(data)
        while let nl = buffer.firstIndex(of: 0x0A) {
            let line = buffer.subdata(in: buffer.startIndex..<nl)
            buffer.removeSubrange(buffer.startIndex...nl)
            guard !line.isEmpty else { continue }
            do {
                let ev = try decoder.decode(ControlEvent.self, from: line)
                DispatchQueue.main.async {
                    MainActor.assumeIsolated { self.onEvent?(ev) }
                }
            } catch {
                logStderr(Data("rex.app: cannot decode event: \(error)\n".utf8))
            }
        }
    }

    private func logStderr(_ data: Data) {
        try? log?.write(contentsOf: data)
        stderrTail.append(data)
        if stderrTail.count > 4096 {
            stderrTail = stderrTail.suffix(4096)
        }
    }

    private func stderrSummary() -> String {
        let text = String(decoding: stderrTail, as: UTF8.self)
        let lines = text.split(separator: "\n").map(String.init)
        // Prefer the "Error: ..." line rexd prints before exiting.
        if let err = lines.first(where: { $0.hasPrefix("Error:") }) {
            return err
        }
        return lines.suffix(3).joined(separator: "\n")
    }
}
