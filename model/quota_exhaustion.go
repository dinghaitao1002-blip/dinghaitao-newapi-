package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// StopChannelForQuotaExhaustion stops the entire channel until an administrator
// re-enables it after replenishing upstream credit. Unlike transient circuit
// failures, a late success or a cooldown must never restore this channel.
func StopChannelForQuotaExhaustion(channelID int) (bool, error) {
	channelStatusLock.Lock()
	defer channelStatusLock.Unlock()

	status := common.ChannelStatusManuallyDisabled
	err := DB.Transaction(func(tx *gorm.DB) error {
		var channel Channel
		if err := lockForUpdate(tx).Select("id", "status", "other_info").First(&channel, channelID).Error; err != nil {
			return err
		}
		if channel.Status != common.ChannelStatusManuallyDisabled {
			info := channel.GetOtherInfo()
			if info == nil {
				info = make(map[string]interface{})
			}
			info["status_reason"] = "Upstream 402: user quota is not enough; recharge and manually enable"
			info["status_time"] = common.GetTimestamp()
			encoded, err := common.Marshal(info)
			if err != nil {
				return err
			}
			channel.OtherInfo = string(encoded)
			if err := tx.Model(&Channel{}).Where("id = ?", channelID).Updates(map[string]interface{}{
				"status": status, "other_info": channel.OtherInfo,
			}).Error; err != nil {
				return err
			}
		} else {
			status = channel.Status
		}
		return tx.Model(&Ability{}).Where("channel_id = ?", channelID).Update("enabled", false).Error
	})
	if err != nil {
		return false, err
	}
	CacheUpdateChannelStatus(channelID, status)
	return true, nil
}
