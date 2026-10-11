package com.servermonitor.api;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import com.servermonitor.repo.Credentials;

import jakarta.servlet.http.HttpServletRequest;

/** /api/credentials CRUD — encrypted SSH secrets stored centrally. */
@RestController
@RequestMapping("/api/credentials")
public class CredentialController {

    private final Credentials credentials;

    public CredentialController(Credentials credentials) {
        this.credentials = credentials;
    }

    private static Map<String, Object> view(Credentials.Credential c) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("id", c.id());
        out.put("user_id", c.userId());
        out.put("name", c.name());
        out.put("ssh_username", c.sshUsername());
        out.put("has_password", c.hasPassword());
        out.put("has_key", c.hasKey());
        out.put("credential_type", c.credentialType());
        out.put("created_at", Jsons.timestamp(c.createdAt()));
        return out;
    }

    public record CredentialBody(String name, String ssh_username, String ssh_password,
        String ssh_key, String credential_type) {
    }

    @GetMapping
    public List<Map<String, Object>> list(HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        return credentials.getCredentialsByUserId(userId).stream().map(CredentialController::view).toList();
    }

    @PostMapping
    public ResponseEntity<Map<String, Object>> create(@RequestBody CredentialBody body,
        HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        NormalizedCredential n = normalize(body, true);
        var created = credentials.create(userId, n.name(), n.username(),
            body.ssh_password() == null ? "" : body.ssh_password(),
            "windows".equals(n.type()) ? "" : (body.ssh_key() == null ? "" : body.ssh_key()),
            n.type());
        return ResponseEntity.status(HttpStatus.CREATED).body(view(created));
    }

    @PutMapping("/{id}")
    public Map<String, Object> update(@PathVariable String id, @RequestBody CredentialBody body,
        HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        UUID credId = UuidParser.parse(id);
        NormalizedCredential n = normalize(body, false);
        boolean updated = credentials.updateCredential(credId, userId, n.name(), n.username(),
            body.ssh_password() == null ? "" : body.ssh_password(),
            "windows".equals(n.type()) ? "" : (body.ssh_key() == null ? "" : body.ssh_key()),
            n.type());
        if (!updated) {
            throw ApiException.notFound("credential not found");
        }
        return Map.of("message", "updated");
    }

    @DeleteMapping("/{id}")
    public Map<String, Object> delete(@PathVariable String id, HttpServletRequest req) {
        UUID userId = AuthSupport.userId(req);
        credentials.deleteCredential(UuidParser.parse(id), userId);
        return Map.of("message", "deleted");
    }

    private record NormalizedCredential(String name, String username, String type) {
    }

    private NormalizedCredential normalize(CredentialBody body, boolean creating) {
        if (body == null) {
            throw ApiException.badRequest("request body is required");
        }
        String name = body.name() == null ? "" : body.name().trim();
        String username = body.ssh_username() == null ? "" : body.ssh_username().trim();
        String type = body.credential_type() == null ? "" : body.credential_type().trim().toLowerCase();
        if (name.isEmpty()) {
            throw ApiException.badRequest("credential name is required");
        }
        if (username.isEmpty()) {
            throw ApiException.badRequest("SSH username is required");
        }
        if (username.chars().anyMatch(c -> Character.isWhitespace(c) || Character.isISOControl(c))) {
            throw ApiException.badRequest("SSH username contains whitespace or control characters");
        }
        if (type.isEmpty()) {
            type = "linux";
        }
        if (!type.equals("linux") && !type.equals("windows")) {
            throw ApiException.badRequest("credential type must be linux or windows");
        }
        if (creating && "windows".equals(type)
            && (body.ssh_password() == null || body.ssh_password().trim().isEmpty())) {
            throw ApiException.badRequest("Windows password is required");
        } else if (creating && !"windows".equals(type)
            && (body.ssh_password() == null || body.ssh_password().isEmpty())
            && (body.ssh_key() == null || body.ssh_key().trim().isEmpty())) {
            throw ApiException.badRequest("SSH password or key is required");
        }
        return new NormalizedCredential(name, username, type);
    }
}
