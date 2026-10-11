package com.servermonitor.crypto;

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.security.SecureRandom;
import java.util.Base64;

import javax.crypto.Cipher;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;

import org.springframework.stereotype.Component;

import com.servermonitor.config.MonitorConfig;

/**
 * AES-256-GCM field encryption, byte-for-byte compatible with the Go crypto
 * package: base64(nonce || ciphertext||tag), 12-byte nonce, 16-byte tag. Data
 * written by either backend decrypts under the other.
 */
@Component
public class FieldCipher {

    private static final int NONCE_SIZE = 12;
    private static final int TAG_BITS = 128;

    private final byte[] key;
    private final SecureRandom random = new SecureRandom();

    public FieldCipher(MonitorConfig cfg) {
        this.key = cfg.getEncryptionKey().getBytes(StandardCharsets.UTF_8);
    }

    public String encrypt(String plaintext) {
        if (plaintext == null || plaintext.isEmpty()) {
            return "";
        }
        try {
            byte[] nonce = new byte[NONCE_SIZE];
            random.nextBytes(nonce);
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
            cipher.init(Cipher.ENCRYPT_MODE, new SecretKeySpec(key, "AES"), new GCMParameterSpec(TAG_BITS, nonce));
            byte[] ciphertext = cipher.doFinal(plaintext.getBytes(StandardCharsets.UTF_8));
            return Base64.getEncoder().encodeToString(ByteBuffer.allocate(nonce.length + ciphertext.length)
                .put(nonce).put(ciphertext).array());
        } catch (Exception e) {
            throw new IllegalStateException("encrypt: " + e.getMessage(), e);
        }
    }

    public String decrypt(String encoded) {
        if (encoded == null || encoded.isEmpty()) {
            return "";
        }
        byte[] blob;
        try {
            blob = Base64.getDecoder().decode(encoded);
        } catch (IllegalArgumentException e) {
            throw new IllegalStateException("decode: " + e.getMessage(), e);
        }
        if (blob.length < NONCE_SIZE) {
            throw new IllegalStateException("ciphertext too short");
        }
        try {
            Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding");
            cipher.init(Cipher.DECRYPT_MODE, new SecretKeySpec(key, "AES"),
                new GCMParameterSpec(TAG_BITS, blob, 0, NONCE_SIZE));
            return new String(cipher.doFinal(blob, NONCE_SIZE, blob.length - NONCE_SIZE), StandardCharsets.UTF_8);
        } catch (Exception e) {
            throw new IllegalStateException("decrypt: " + e.getMessage(), e);
        }
    }
}
