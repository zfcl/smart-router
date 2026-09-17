package com.zfcl.smartrouter;

import android.content.Context;
import org.json.JSONObject;
import java.io.File;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;

final class RouterPrefs {
    private RouterPrefs() {}

    static File configRoot(Context context) {
        File dir = new File(context.getFilesDir(), "config");
        if (!dir.exists()) dir.mkdirs();
        return dir;
    }

    static int readPort(Context context) {
        try {
            File f = new File(new File(configRoot(context), "SmartRouter"), "config.json");
            if (!f.isFile()) return 8787;
            String text = new String(Files.readAllBytes(f.toPath()), StandardCharsets.UTF_8);
            int port = new JSONObject(text).optInt("port", 8787);
            return port >= 1024 && port <= 65535 ? port : 8787;
        } catch (Exception ignored) {
            return 8787;
        }
    }

    static String baseUrl(Context context) {
        return "http://127.0.0.1:" + readPort(context);
    }
}
