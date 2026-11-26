// The Kotlin of the native module qrscanner: an Android library, which mygo
// build adds to the app's Gradle build.
plugins {
    id("com.android.library")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.github.chialecode.trace.cmd.trace_mobile.internal.qrscanner"
}

dependencies {
    implementation("androidx.camera:camera-core:1.6.2")
    implementation("androidx.camera:camera-camera2:1.6.2")
    implementation("androidx.camera:camera-lifecycle:1.6.2")
    implementation("androidx.camera:camera-view:1.6.2")
    implementation("com.google.zxing:core:3.5.4")
}
