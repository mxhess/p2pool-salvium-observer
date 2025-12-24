// P2Pool Salvium Observer - Shared JavaScript
// API base URL - relative path works from any subdirectory
const API_BASE = 'api';

// Utility functions
const Utils = {
    // Format hashrate with appropriate units
    formatHashrate(h) {
        if (h >= 1e12) return (h / 1e12).toFixed(2) + ' TH/s';
        if (h >= 1e9) return (h / 1e9).toFixed(2) + ' GH/s';
        if (h >= 1e6) return (h / 1e6).toFixed(2) + ' MH/s';
        if (h >= 1e3) return (h / 1e3).toFixed(2) + ' KH/s';
        return h.toFixed(2) + ' H/s';
    },

    // Format SAL amount (atomic units to SAL)
    formatSAL(atomic) {
        return (atomic / 1e8).toFixed(8) + ' SAL';
    },

    // Format SAL amount short
    formatSALShort(atomic) {
        return (atomic / 1e8).toFixed(4) + ' SAL';
    },

    // Format difficulty
    formatDifficulty(d) {
        if (d >= 1e12) return (d / 1e12).toFixed(2) + ' T';
        if (d >= 1e9) return (d / 1e9).toFixed(2) + ' G';
        if (d >= 1e6) return (d / 1e6).toFixed(2) + ' M';
        if (d >= 1e3) return (d / 1e3).toFixed(2) + ' K';
        return d.toString();
    },

    // Format percentage
    formatPercent(value, decimals = 2) {
        return value.toFixed(decimals) + '%';
    },

    // Format time ago
    timeAgo(timestamp) {
        const seconds = Math.floor(Date.now() / 1000 - timestamp);
        if (seconds < 60) return seconds + 's ago';
        if (seconds < 3600) return Math.floor(seconds / 60) + 'm ago';
        if (seconds < 86400) return Math.floor(seconds / 3600) + 'h ago';
        return Math.floor(seconds / 86400) + 'd ago';
    },

    // Format date
    formatDate(timestamp) {
        return new Date(timestamp * 1000).toLocaleString();
    },

    // Truncate hash for display
    truncateHash(hash, chars = 8) {
        if (!hash || hash.length <= chars * 2 + 3) return hash;
        return hash.substring(0, chars) + '...' + hash.substring(hash.length - chars);
    },

    // Truncate address to show first 8 and last 8 chars
    // Format: XXXXXXXX...XXXXXXXX (8 chars each side)
    truncateAddress(addr, chars = 8) {
        if (!addr || addr === '--') return '--';

        // If already properly truncated (8...8), return as-is
        if (addr.includes('...')) {
            const parts = addr.split('...');
            if (parts.length === 2 && parts[0].length === chars && parts[1].length === chars) {
                return addr;
            }
            // Improperly truncated - just return what we have
            return addr;
        }

        // Full address - truncate it
        if (addr.length > chars * 2 + 3) {
            return addr.substring(0, chars) + '...' + addr.substring(addr.length - chars);
        }

        // Short address - return as-is
        return addr;
    },

    // Effort color class
    effortClass(effort) {
        if (effort < 100) return 'effort-low';
        if (effort < 200) return 'effort-normal';
        return 'effort-high';
    },

    // Parse URL parameters
    getUrlParams() {
        const params = {};
        const searchParams = new URLSearchParams(window.location.search);
        for (const [key, value] of searchParams) {
            params[key] = value;
        }
        return params;
    }
};

// API wrapper
const API = {
    async get(endpoint) {
        try {
            const response = await fetch(API_BASE + endpoint);
            if (!response.ok) {
                throw new Error(`HTTP ${response.status}`);
            }
            return await response.json();
        } catch (error) {
            console.error(`API error (${endpoint}):`, error);
            return null;
        }
    },

    // Pool info
    async getPoolInfo() {
        return this.get('/pool_info');
    },

    // Found blocks
    async getFoundBlocks(limit = 20) {
        return this.get(`/found_blocks?limit=${limit}`);
    },

    // Side blocks (recent shares)
    async getSideBlocks(limit = 50) {
        return this.get(`/side_blocks?limit=${limit}`);
    },

    // PPLNS window data
    async getPPLNS() {
        return this.get('/pplns');
    },

    // Miner info
    async getMinerInfo(address) {
        return this.get(`/miner_info/${encodeURIComponent(address)}`);
    },

    // Miner shares
    async getMinerShares(address) {
        return this.get(`/miner/${encodeURIComponent(address)}/shares`);
    },

    // Miner payouts
    async getMinerPayouts(address) {
        return this.get(`/payouts/${encodeURIComponent(address)}`);
    },

    // Block by ID
    async getBlock(id) {
        return this.get(`/block_by_id/${id}`);
    },

    // Network stats
    async getNetworkStats() {
        return this.get('/network/stats');
    },

    // Pool stats
    async getPoolStats() {
        return this.get('/pool/stats');
    },

    // Found block details (with PPLNS snapshot and payouts)
    async getFoundBlockDetails(height) {
        return this.get(`/found_block/${height}`);
    }
};

// Theme toggle
function initTheme() {
    const saved = localStorage.getItem('theme');
    if (saved === 'dark' || (!saved && window.matchMedia('(prefers-color-scheme: dark)').matches)) {
        document.body.classList.add('dark');
    }
}

function toggleTheme() {
    document.body.classList.toggle('dark');
    localStorage.setItem('theme', document.body.classList.contains('dark') ? 'dark' : 'light');
}

// Auto-refresh functionality
let refreshInterval = null;

function startAutoRefresh(callback, intervalMs = 30000) {
    if (refreshInterval) clearInterval(refreshInterval);
    refreshInterval = setInterval(callback, intervalMs);
}

function stopAutoRefresh() {
    if (refreshInterval) {
        clearInterval(refreshInterval);
        refreshInterval = null;
    }
}

// Initialize on page load
document.addEventListener('DOMContentLoaded', () => {
    initTheme();
});

// Salvium address validation regex
const SALVIUM_ADDRESS_REGEX = /^SC1[1-9A-HJ-NP-Za-km-z]{94,140}$/;

function isValidSalviumAddress(address) {
    return SALVIUM_ADDRESS_REGEX.test(address);
}
