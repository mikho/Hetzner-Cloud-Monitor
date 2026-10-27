// swift-tools-version:5.10
import PackageDescription

let package = Package(
    name: "HetznerDashboard",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(name: "HetznerDashboard", path: "Sources/HetznerDashboard")
    ]
)
