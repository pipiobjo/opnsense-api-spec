<?php
// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

namespace OPNsense\Demo;

use OPNsense\Base\BaseModel;

/* saves through its own code, like OPNsense\Bind\Domain */
class Demo extends BaseModel
{
    public function serializeToConfig($validateFullModel = false, $disable_validation = false)
    {
        return parent::serializeToConfig($validateFullModel, $disable_validation);
    }
}
