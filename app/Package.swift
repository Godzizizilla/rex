// swift-tools-version:5.10
import PackageDescription

let package = Package(
    name: "Rex",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "Rex",
            path: "Sources/Rex",
            linkerSettings: [.linkedFramework("Carbon")]
        ),
    ]
)
