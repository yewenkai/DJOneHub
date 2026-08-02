// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "DJOneHubNotifier",
    platforms: [.macOS(.v13)],
    products: [
        .executable(name: "DJOneHubNotifier", targets: ["DJOneHubNotifier"]),
    ],
    targets: [
        .executableTarget(
            name: "DJOneHubNotifier",
            path: "Sources/DJOneHubNotifier"
        ),
    ]
)
