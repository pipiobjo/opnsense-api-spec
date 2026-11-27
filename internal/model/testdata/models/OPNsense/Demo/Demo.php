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

    /* rules over several fields, like OPNsense\Firewall\Filter */
    public function performValidation($validateFullModel = false)
    {
        $messages = parent::performValidation($validateFullModel);
        $this->checkSafeSearch($messages);
        foreach ($this->hosts->host->iterateItems() as $host) {
            if (!$host->ttl->isEmpty() && (string)$host->{'mode'} == '') {
                $messages->appendMessage(new Message('mode needed', $host->ttl->__reference));
            }
            $this->zone($host); /* a method call, not the field zone */
            $messages->appendMessage(new Message('x', $host->__reference . ".name"));
            foreach (['hostname'] as $f) {
                $host->$f->getNodeData();
            }
        }
        return $messages;
    }

    /* a helper performValidation calls, as os-caddy's checkLayer4Matchers */
    private function checkSafeSearch($messages)
    {
        if ($this->general->safesearch->isEqual('1')) {
            $messages->appendMessage(new Message('x', 'general.safesearch'));
        }
    }

    /* fills values from elsewhere on every load, like OPNsense\Core\InitialSetup */
    protected function init()
    {
        $this->general->enabled = '1';
    }
}
