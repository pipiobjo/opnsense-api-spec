<?php
// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

namespace OPNsense\Demo\FieldTypes;

use OPNsense\Base\FieldTypes\BaseListField;

/* options from the backend at run time (as os-ddclient's ServiceField) */
class BackendServiceField extends BaseListField
{
    private static $cache = [];

    protected function actionPostLoadingEvent()
    {
        $this->internalOptionList = self::$cache;
    }
}

/* its own default (as IPsec's CharonLogLevelField) */
class LogLevelField extends BaseListField
{
    protected $internalDefaultValue = "1";
}
