<?php
// Fixture modelled on os-acme-client AccountsController (BSD-2-Clause), shortened.
namespace OPNsense\Demo\Api;

use OPNsense\Base\ApiMutableModelControllerBase;

class AccountsController extends ApiMutableModelControllerBase
{
    protected static $internalModelName = 'demo';
    protected static $internalModelClass = 'OPNsense\Demo\Demo';

    public function getAction($uuid = null)
    {
        return $this->getBase('host', 'hosts.host', $uuid);
    }

    public function updateAction($uuid)
    {
        return $this->setBase('host', 'hosts.host', $uuid);
    }
}
