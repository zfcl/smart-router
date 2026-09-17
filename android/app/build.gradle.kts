plugins { id("com.android.application") }

android {
    namespace = "com.zfcl.smartrouter"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.zfcl.smartrouter"
        minSdk = 26
        targetSdk = 35
        versionCode = 110
        versionName = "1.1.0"
        ndk { abiFilters += listOf("arm64-v8a") }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    sourceSets["main"].jniLibs.srcDirs("src/main/jniLibs")

    packaging {
        jniLibs {
            useLegacyPackaging = true
            keepDebugSymbols += setOf("**/libsmart_router_exec.so")
        }
    }
}
