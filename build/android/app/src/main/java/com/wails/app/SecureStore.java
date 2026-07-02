package com.wails.app;

import android.content.Context;
import android.content.SharedPreferences;

import androidx.security.crypto.EncryptedSharedPreferences;
import androidx.security.crypto.MasterKey;

/**
 * SecureStore wraps EncryptedSharedPreferences - values are encrypted at
 * rest with a key held in the Android Keystore (hardware-backed where
 * available). Used to persist the vault passphrase for biometric
 * auto-unlock. The biometric gate itself is enforced separately by
 * BiometricPrompt in WailsBridge; this layer guarantees the stored secret
 * is unreadable without the device Keystore (resists file/backup theft).
 */
class SecureStore {
    private static final String PREFS_NAME = "ssh_tool_secure";

    private final SharedPreferences prefs;

    SecureStore(Context context) {
        try {
            MasterKey masterKey = new MasterKey.Builder(context)
                    .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
                    .build();
            prefs = EncryptedSharedPreferences.create(
                    context,
                    PREFS_NAME,
                    masterKey,
                    EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                    EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM);
        } catch (Exception e) {
            throw new RuntimeException("failed to open secure store", e);
        }
    }

    void set(String key, String value) {
        prefs.edit().putString(key, value).apply();
    }

    String get(String key) {
        return prefs.getString(key, null);
    }

    void delete(String key) {
        prefs.edit().remove(key).apply();
    }
}
